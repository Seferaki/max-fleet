"""Бизнес-команды POST /commands. Каждая выполняется внутри одной транзакции.

Поведение версий и шагов совпадает с Go data-mock (services/gateway/internal/datamock),
чтобы на INT Go получил те же ответы, что и при разработке.
"""

from __future__ import annotations

import secrets
import uuid
from collections.abc import Callable
from datetime import datetime, timedelta
from decimal import Decimal
from typing import Any

from sqlalchemy import select
from sqlalchemy.orm import Session

from app import models as m
from app.api import schemas as s
from app.config import Settings
from app.domain import dto
from app.domain.core import (
    Actor,
    assignment_of_employee,
    audit,
    check_version,
    expire_attempt,
    lock,
    lock_assignment_for_vehicle,
    lock_employee,
    lock_vehicle,
    outbox,
    touch,
)
from app.errors import DomainError

ADMIN_PURPOSES = {
    "vehicle_block": "vehicle.block",
    "vehicle_unblock": "vehicle.unblock",
    "employee_grant": "employee.grant",
    "employee_access": "employee.access",
    "admin_close": "trip.admin_close",
}
INTENT_FIELDS = {
    "vehicle.block": {"operation", "target_id", "expected_version", "reason"},
    "vehicle.unblock": {"operation", "target_id", "expected_version", "reason", "review_completed"},
    "employee.grant": {"operation", "target_id", "expected_version", "max_user_id", "display_name"},
    "employee.access": {"operation", "target_id", "expected_version", "can_start_trip", "reason"},
    "trip.admin_close": {"operation", "target_id", "expected_version", "reason"},
}


def result(operation: str, aggregate: dict[str, Any], *, correct: bool | None = None,
           attempts_remaining: int | None = None, proof: uuid.UUID | None = None) -> dict[str, Any]:
    return {"operation": operation, "aggregate": aggregate, "correct": correct,
            "attempts_remaining": attempts_remaining,
            "challenge_proof_id": str(proof) if proof else None}


class Ctx:
    def __init__(self, session: Session, actor: Actor, cmd: s.Command, now: datetime,
                 settings: Settings) -> None:
        self.session = session
        self.actor = actor
        self.cmd = cmd
        self.now = now
        self.settings = settings

    @property
    def target(self) -> uuid.UUID:
        assert self.cmd.target_id is not None
        return self.cmd.target_id


def require_admin(ctx: Ctx) -> None:
    if not ctx.actor.is_admin:
        raise DomainError("ADMIN_REQUIRED")


def missing_slots(session: Session, inspection_id: uuid.UUID) -> list[int]:
    occupied = dto.occupied_slots(session, inspection_id)
    return [slot for slot in dto.SLOTS if slot not in occupied]


# ------------------------------------------------------------------ контексты осмотра

class InspectionContext:
    """Осмотр вместе с владельческим оформлением/возвратом, заблокированные по порядку."""

    def __init__(self) -> None:
        self.inspection: m.Inspection
        self.checkout: m.CheckoutAttempt | None = None
        self.ret: m.ReturnAttempt | None = None
        self.trip: m.Trip | None = None
        self.vehicle: m.Vehicle
        self.assignment: m.VehicleAssignment | None = None
        self.owner_id: uuid.UUID

    def bump_parent(self, now: datetime) -> None:
        if self.checkout is not None:
            touch(self.checkout, now)
        elif self.ret is not None:
            touch(self.ret, now)


def load_inspection_context(session: Session, actor: Actor, inspection_id: uuid.UUID,
                            *, allow_admin: bool = False) -> InspectionContext:
    insp = session.get(m.Inspection, inspection_id)
    if insp is None:
        raise DomainError("NOT_FOUND")
    ic = InspectionContext()
    if insp.checkout_attempt_id is not None:
        attempt = session.get(m.CheckoutAttempt, insp.checkout_attempt_id)
        assert attempt is not None
        owner, vehicle_id = attempt.employee_id, attempt.vehicle_id
    else:
        ret = session.get(m.ReturnAttempt, insp.return_attempt_id)
        assert ret is not None
        trip = session.get(m.Trip, ret.trip_id)
        assert trip is not None
        owner, vehicle_id = trip.employee_id, trip.vehicle_id
    if owner != actor.id and not (allow_admin and actor.is_admin):
        raise DomainError("NOT_FOUND")
    lock_employee(session, owner)
    ic.vehicle = lock_vehicle(session, vehicle_id)
    ic.assignment = lock_assignment_for_vehicle(session, vehicle_id)
    if insp.checkout_attempt_id is not None:
        ic.checkout = lock(session, m.CheckoutAttempt, insp.checkout_attempt_id)
    else:
        ret_row = session.get(m.ReturnAttempt, insp.return_attempt_id)
        assert ret_row is not None
        ic.trip = lock(session, m.Trip, ret_row.trip_id)
        ic.ret = lock(session, m.ReturnAttempt, insp.return_attempt_id)
    ic.inspection = lock(session, m.Inspection, inspection_id)
    ic.owner_id = owner
    return ic


def ensure_hold_alive(ctx_session: Session, ic: InspectionContext, now: datetime) -> None:
    """TTL проверяется на каждой операции независимо от фоновой очистки."""
    attempt = ic.checkout
    if attempt is None:
        return
    if attempt.status == "holding" and attempt.expires_at <= now:
        expire_attempt(ctx_session, attempt, ic.vehicle, ic.assignment, now)
        ctx_session.flush()
    if attempt.status == "expired":
        raise DomainError("HOLD_EXPIRED", current_version=ic.inspection.version)


def ensure_inspection_editable(ic: InspectionContext) -> None:
    if ic.checkout is not None:
        if ic.checkout.status != "holding":
            raise DomainError("INVALID_STATE")
    else:
        assert ic.ret is not None and ic.trip is not None
        if ic.ret.status != "draft" or ic.ret.intent_confirmed_at is None or ic.trip.status != "returning":
            raise DomainError("INVALID_STATE")
    if ic.inspection.status != "draft":
        raise DomainError("INVALID_STATE")


# ------------------------------------------------------------------ оформление

def checkout_create(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    employee = lock_employee(session, ctx.actor.id)
    if not employee.can_start_trip:
        raise DomainError("CANNOT_START_TRIP")
    vehicle = lock_vehicle(session, ctx.target)
    own = assignment_of_employee(session, employee.id)
    if own is not None:
        attempt = session.get(m.CheckoutAttempt, own.checkout_attempt_id)
        if own.phase == "hold" and attempt is not None and attempt.expires_at <= now:
            if own.vehicle_id != vehicle.id:
                other = lock_vehicle(session, own.vehicle_id)
            else:
                other = vehicle
            expire_attempt(session, lock(session, m.CheckoutAttempt, attempt.id), other, own, now)
            session.flush()
        else:
            raise DomainError("USER_BUSY")
    assignment = lock_assignment_for_vehicle(session, vehicle.id)
    if assignment is not None:
        attempt = session.get(m.CheckoutAttempt, assignment.checkout_attempt_id)
        if assignment.phase == "hold" and attempt is not None and attempt.expires_at <= now:
            lock_employee(session, assignment.employee_id)
            expire_attempt(session, lock(session, m.CheckoutAttempt, attempt.id), vehicle, assignment, now)
            session.flush()
        else:
            raise DomainError("VEHICLE_UNAVAILABLE")
    if not dto.vehicle_issuable(session, vehicle):
        raise DomainError("VEHICLE_UNAVAILABLE")
    check_version(vehicle, ctx.cmd.expected_version)
    attempt = m.CheckoutAttempt(
        id=uuid.uuid4(), employee_id=employee.id, vehicle_id=vehicle.id, status="holding", step="math",
        expires_at=now + timedelta(minutes=ctx.settings.hold_minutes), version=1,
        created_at=now, updated_at=now)
    session.add(attempt)
    session.flush()
    session.add(m.Inspection(id=uuid.uuid4(), phase="before", status="draft", checkout_attempt_id=attempt.id,
                             author_id=employee.id, version=1, created_at=now, updated_at=now))
    session.add(m.VehicleAssignment(vehicle_id=vehicle.id, employee_id=employee.id,
                                    checkout_attempt_id=attempt.id, phase="hold",
                                    hold_expires_at=attempt.expires_at, version=1,
                                    created_at=now, updated_at=now))
    touch(vehicle, now)
    audit(session, ctx.actor, "checkout.create", "checkout_attempt", attempt.id,
          after={"vehicle_id": str(vehicle.id)})
    session.flush()
    return result("checkout.create", dto.checkout_dto(session, attempt))


def _lock_own_checkout(ctx: Ctx) -> tuple[m.CheckoutAttempt, m.Vehicle, m.VehicleAssignment | None]:
    session = ctx.session
    attempt = session.get(m.CheckoutAttempt, ctx.target)
    if attempt is None or attempt.employee_id != ctx.actor.id:
        raise DomainError("NOT_FOUND")
    lock_employee(session, attempt.employee_id)
    vehicle = lock_vehicle(session, attempt.vehicle_id)
    assignment = lock_assignment_for_vehicle(session, attempt.vehicle_id)
    attempt = lock(session, m.CheckoutAttempt, attempt.id)
    if attempt.status == "holding" and attempt.expires_at <= ctx.now:
        expire_attempt(session, attempt, vehicle, assignment, ctx.now)
        session.flush()
        assignment = None
    return attempt, vehicle, assignment


def checkout_cancel(ctx: Ctx) -> dict[str, Any]:
    attempt, vehicle, assignment = _lock_own_checkout(ctx)
    if attempt.status == "expired":
        raise DomainError("HOLD_EXPIRED", current_version=attempt.version)
    check_version(attempt, ctx.cmd.expected_version)
    if attempt.status != "holding":
        raise DomainError("INVALID_STATE")
    now = ctx.now
    attempt.status = "cancelled"
    attempt.ended_at = now
    attempt.end_reason = "cancelled_by_employee"
    touch(attempt, now)
    insp = dto.checkout_inspection(ctx.session, attempt.id)
    if insp.status == "draft":
        insp.status = "abandoned"
        insp.updated_at = now
    if assignment is not None and assignment.checkout_attempt_id == attempt.id:
        ctx.session.delete(assignment)
    touch(vehicle, now)
    audit(ctx.session, ctx.actor, "checkout.cancel", "checkout_attempt", attempt.id)
    ctx.session.flush()
    return result("checkout.cancel", dto.checkout_dto(ctx.session, attempt))


def checkout_accept_rules(ctx: Ctx) -> dict[str, Any]:
    attempt, _vehicle, _assignment = _lock_own_checkout(ctx)
    if attempt.status == "expired":
        raise DomainError("HOLD_EXPIRED", current_version=attempt.version)
    check_version(attempt, ctx.cmd.expected_version)
    if attempt.status != "holding" or attempt.intent_confirmed_at is None or attempt.rules_accepted_at is not None:
        raise DomainError("INVALID_STATE")
    rules = ctx.session.scalar(select(m.RulesVersion).where(m.RulesVersion.is_current.is_(True)))
    if rules is None or rules.id != ctx.cmd.payload.rules_version_id:
        raise DomainError("STALE_VERSION")
    attempt.rules_version_id = rules.id
    attempt.rules_accepted_at = ctx.now
    attempt.step = "inspection"
    touch(attempt, ctx.now)
    audit(ctx.session, ctx.actor, "checkout.accept_rules", "checkout_attempt", attempt.id,
          after={"rules_version_id": str(rules.id)})
    ctx.session.flush()
    return result("checkout.accept_rules", dto.checkout_dto(ctx.session, attempt))


def checkout_set_no_new_issues(ctx: Ctx) -> dict[str, Any]:
    attempt, _vehicle, _assignment = _lock_own_checkout(ctx)
    if attempt.status == "expired":
        raise DomainError("HOLD_EXPIRED", current_version=attempt.version)
    check_version(attempt, ctx.cmd.expected_version)
    insp = dto.checkout_inspection(ctx.session, attempt.id)
    if attempt.status != "holding" or attempt.rules_accepted_at is None or insp.new_damage is True:
        raise DomainError("INVALID_STATE")
    attempt.no_new_issues = True
    touch(attempt, ctx.now)
    ctx.session.flush()
    return result("checkout.set_no_new_issues", dto.checkout_dto(ctx.session, attempt))


def checkout_start(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    attempt = session.get(m.CheckoutAttempt, ctx.target)
    if attempt is None or attempt.employee_id != ctx.actor.id:
        raise DomainError("NOT_FOUND")
    employee = lock_employee(session, attempt.employee_id)
    vehicle = lock_vehicle(session, attempt.vehicle_id)
    assignment = lock_assignment_for_vehicle(session, attempt.vehicle_id)
    attempt = lock(session, m.CheckoutAttempt, attempt.id)
    if attempt.status == "holding" and attempt.expires_at <= now:
        expire_attempt(session, attempt, vehicle, assignment, now)
        session.flush()
    if attempt.status == "expired":
        raise DomainError("HOLD_EXPIRED", current_version=attempt.version)
    check_version(attempt, ctx.cmd.expected_version)
    insp = lock(session, m.Inspection, dto.checkout_inspection(session, attempt.id).id)
    open_trip = session.scalar(select(m.Trip.id).where(m.Trip.employee_id == employee.id,
                                                       m.Trip.status.in_(("active", "returning"))))
    if attempt.status != "holding" or insp.status != "draft" or open_trip is not None:
        raise DomainError("INVALID_STATE")
    if not employee.can_start_trip:
        raise DomainError("CANNOT_START_TRIP")
    current_rules = session.scalar(select(m.RulesVersion.id).where(m.RulesVersion.is_current.is_(True)))
    if attempt.intent_confirmed_at is None or attempt.rules_accepted_at is None \
            or attempt.rules_version_id != current_rules:
        raise DomainError("RULES_REQUIRED")
    missing = missing_slots(session, insp.id)
    if missing:
        raise DomainError("PHOTO_SET_INCOMPLETE", missing_slots=missing)
    if insp.photos_confirmed_at is None or insp.fuel_level is None or insp.odometer_km is None \
            or attempt.no_new_issues is not True or insp.new_damage is True:
        raise DomainError("INVALID_STATE")
    if assignment is None or assignment.checkout_attempt_id != attempt.id or assignment.phase != "hold" \
            or vehicle.manual_blocked or vehicle.needs_review or dto.has_blocking_issue(session, vehicle.id):
        raise DomainError("VEHICLE_UNAVAILABLE")
    if vehicle.current_odometer_km is not None and insp.odometer_km < vehicle.current_odometer_km:
        raise DomainError("ODOMETER_ROLLBACK")
    trip = m.Trip(id=uuid.uuid4(), checkout_attempt_id=attempt.id, employee_id=employee.id,
                  vehicle_id=vehicle.id, status="active", started_at=now, missing_data=[], version=1,
                  created_at=now, updated_at=now)
    session.add(trip)
    session.flush()
    attempt.status = "started"
    attempt.step = "active_trip"
    attempt.ended_at = now
    attempt.end_reason = "trip_started"
    touch(attempt, now)
    insp.status = "finalized"
    insp.attested_at = now
    insp.finalized_at = now
    touch(insp, now)
    assignment.phase = "trip"
    assignment.trip_id = trip.id
    assignment.hold_expires_at = None
    touch(assignment, now)
    touch(employee, now)
    vehicle.current_fuel = insp.fuel_level
    vehicle.current_odometer_km = insp.odometer_km
    vehicle.fuel_confirmed_at = now
    vehicle.odometer_confirmed_at = now
    vehicle.last_inspection_id = insp.id
    touch(vehicle, now)
    audit(session, ctx.actor, "trip.start", "trip", trip.id,
          after={"vehicle_id": str(vehicle.id), "fuel_level": insp.fuel_level, "odometer_km": insp.odometer_km})
    outbox(session, "trip_started", "trip", trip.id, trip.version, vehicle_id=vehicle.id, reason=None, now=now)
    session.flush()
    return result("checkout.start", dto.trip_dto(session, trip))


# ------------------------------------------------------------------ math challenge

def _intent_sha(intent: dict[str, Any]) -> str:
    return s.canonical_sha(intent)


def challenge_create(ctx: Ctx) -> dict[str, Any]:
    session, now, payload = ctx.session, ctx.now, ctx.cmd.payload
    intent = payload.intent_payload
    intent_raw = ctx.cmd.raw_payload["intent_payload"]
    expires = now + timedelta(minutes=ctx.settings.challenge_minutes)
    target_type = "none"
    target_id: uuid.UUID | None = ctx.cmd.target_id
    target_version: int | None = ctx.cmd.expected_version
    invalidate_filter: Any
    if payload.purpose in ("take", "return"):
        if target_id is None or intent.target_id is None or intent.expected_version is None \
                or set(intent_raw) != {"operation", "target_id", "expected_version"}:
            raise DomainError("INVALID_REQUEST")
        if payload.purpose == "take":
            if intent.operation != "checkout.create":
                raise DomainError("INVALID_REQUEST")
            attempt = session.get(m.CheckoutAttempt, target_id)
            if attempt is None or attempt.employee_id != ctx.actor.id:
                raise DomainError("NOT_FOUND")
            lock_employee(session, attempt.employee_id)
            vehicle = lock_vehicle(session, attempt.vehicle_id)
            assignment = lock_assignment_for_vehicle(session, attempt.vehicle_id)
            attempt = lock(session, m.CheckoutAttempt, attempt.id)
            if attempt.status == "holding" and attempt.expires_at <= now:
                expire_attempt(session, attempt, vehicle, assignment, now)
                session.flush()
            check_version(attempt, ctx.cmd.expected_version)
            if attempt.status != "holding" or attempt.intent_confirmed_at is not None \
                    or attempt.vehicle_id != intent.target_id:
                raise DomainError("INVALID_STATE")
            # Go передаёт версию машины до checkout.create (как в data-mock).
            if intent.expected_version != vehicle.version - 1:
                raise DomainError("STALE_VERSION")
            expires = min(expires, attempt.expires_at)
            target_type = "checkout_attempt"
        else:
            if intent.operation != "trip.begin_return":
                raise DomainError("INVALID_REQUEST")
            ret = session.get(m.ReturnAttempt, target_id)
            trip = session.get(m.Trip, ret.trip_id) if ret else None
            if ret is None or trip is None or trip.employee_id != ctx.actor.id:
                raise DomainError("NOT_FOUND")
            lock_employee(session, trip.employee_id)
            lock_vehicle(session, trip.vehicle_id)
            trip = lock(session, m.Trip, trip.id)
            ret = lock(session, m.ReturnAttempt, ret.id)
            check_version(ret, ctx.cmd.expected_version)
            current = dto.trip_current_return(session, trip)
            if ret.status != "draft" or ret.intent_confirmed_at is not None or trip.status != "returning" \
                    or current is None or current.id != ret.id or trip.id != intent.target_id:
                raise DomainError("INVALID_STATE")
            if intent.expected_version != trip.version - 1:
                raise DomainError("STALE_VERSION")
            target_type = "return_attempt"
        invalidate_filter = (m.Challenge.target_id == target_id)
    else:
        require_admin(ctx)
        operation = ADMIN_PURPOSES[payload.purpose]
        if intent.operation != operation or set(intent_raw) != INTENT_FIELDS[operation] \
                or any(intent_raw[k] is None for k in INTENT_FIELDS[operation]
                       if k not in ("target_id", "expected_version")):
            raise DomainError("INVALID_REQUEST")
        if (intent.target_id, intent.expected_version) != (target_id, target_version):
            raise DomainError("INVALID_REQUEST")
        if operation == "employee.grant":
            if target_id is not None:
                raise DomainError("INVALID_REQUEST")
            target_type = "employee"
        else:
            if target_id is None:
                raise DomainError("INVALID_REQUEST")
            model = {"vehicle.block": m.Vehicle, "vehicle.unblock": m.Vehicle,
                     "employee.access": m.Employee, "trip.admin_close": m.Trip}[operation]
            obj = session.get(model, target_id)
            if obj is None:
                raise DomainError("NOT_FOUND")
            check_version(obj, target_version)
            target_type = model.__tablename__
        invalidate_filter = (m.Challenge.purpose == payload.purpose) & (
            m.Challenge.target_id == target_id if target_id is not None else m.Challenge.target_id.is_(None))
    stale = session.scalars(select(m.Challenge).where(
        m.Challenge.actor_id == ctx.actor.id, invalidate_filter,
        m.Challenge.solved_at.is_(None), m.Challenge.invalidated_at.is_(None)).with_for_update()).all()
    for old in stale:
        old.invalidated_at = now
        touch(old, now)
    a, b = secrets.randbelow(9) + 1, secrets.randbelow(9) + 1
    total = a + b
    start = max(0, min(total - 2, 15))
    options = [start, start + 1, start + 2, start + 3]
    for i in range(len(options) - 1, 0, -1):
        j = secrets.randbelow(i + 1)
        options[i], options[j] = options[j], options[i]
    challenge = m.Challenge(
        id=uuid.uuid4(), actor_id=ctx.actor.id, purpose=payload.purpose, target_type=target_type,
        target_id=target_id, target_version=target_version, intent_payload=intent_raw,
        intent_sha256=_intent_sha(intent_raw), operand_a=a, operand_b=b, options=options,
        correct_answer=options.index(total), wrong_attempts=0, expires_at=expires, version=1,
        created_at=now, updated_at=now)
    session.add(challenge)
    session.flush()
    return result("challenge.create", dto.challenge_dto(challenge))


def challenge_answer(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    ch = session.get(m.Challenge, ctx.target)
    if ch is None or ch.actor_id != ctx.actor.id:
        raise DomainError("NOT_FOUND")
    attempt: m.CheckoutAttempt | None = None
    ret: m.ReturnAttempt | None = None
    if ch.purpose == "take":
        pre = session.get(m.CheckoutAttempt, ch.target_id)
        assert pre is not None
        lock_employee(session, pre.employee_id)
        vehicle = lock_vehicle(session, pre.vehicle_id)
        assignment = lock_assignment_for_vehicle(session, pre.vehicle_id)
        attempt = lock(session, m.CheckoutAttempt, pre.id)
        if attempt.status == "holding" and attempt.expires_at <= now:
            expire_attempt(session, attempt, vehicle, assignment, now)
            session.flush()
    elif ch.purpose == "return":
        pre_ret = session.get(m.ReturnAttempt, ch.target_id)
        assert pre_ret is not None
        trip = session.get(m.Trip, pre_ret.trip_id)
        assert trip is not None
        lock_employee(session, trip.employee_id)
        lock_vehicle(session, trip.vehicle_id)
        lock(session, m.Trip, trip.id)
        ret = lock(session, m.ReturnAttempt, pre_ret.id)
    ch = lock(session, m.Challenge, ch.id)
    check_version(ch, ctx.cmd.expected_version)
    if now >= ch.expires_at:
        raise DomainError("CHALLENGE_EXPIRED")
    if ch.invalidated_at is not None or ch.solved_at is not None or ch.wrong_attempts >= 3:
        raise DomainError("INVALID_STATE")
    if attempt is not None and (attempt.status != "holding" or attempt.version != ch.target_version):
        raise DomainError("INVALID_STATE")
    if ret is not None and (ret.status != "draft" or ret.version != ch.target_version):
        raise DomainError("INVALID_STATE")
    correct = ctx.cmd.payload.selected_option == ch.correct_answer
    proof: uuid.UUID | None = None
    if not correct:
        ch.wrong_attempts += 1
    else:
        ch.solved_at = now
        if attempt is not None:
            attempt.intent_confirmed_at = now
            attempt.step = "rules"
            touch(attempt, now)
            ch.consumed_at = now
        elif ret is not None:
            ret.intent_confirmed_at = now
            ret.step = "checklist"
            touch(ret, now)
            ch.consumed_at = now
        else:
            proof = ch.id
    touch(ch, now)
    session.flush()
    return result("challenge.answer", dto.challenge_dto(ch), correct=correct,
                  attempts_remaining=3 - ch.wrong_attempts, proof=proof)


def consume_admin_challenge(ctx: Ctx, challenge_id: uuid.UUID, purpose: str,
                            intent: dict[str, Any]) -> None:
    ch = lock(ctx.session, m.Challenge, challenge_id)
    if ch is None or ch.actor_id != ctx.actor.id or ch.purpose != purpose:
        raise DomainError("CHALLENGE_INVALID")
    if ch.solved_at is None or ch.consumed_at is not None or ch.invalidated_at is not None:
        raise DomainError("CHALLENGE_INVALID")
    if ctx.now >= ch.expires_at:
        raise DomainError("CHALLENGE_EXPIRED")
    if ch.intent_sha256 != _intent_sha(intent):
        raise DomainError("CHALLENGE_INVALID")
    ch.consumed_at = ctx.now
    touch(ch, ctx.now)


# ------------------------------------------------------------------ осмотр

def inspection_update(ctx: Ctx) -> dict[str, Any]:
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    if not ctx.cmd.raw_payload:
        raise DomainError("INVALID_REQUEST")
    ic = load_inspection_context(session, ctx.actor, ctx.target)
    ensure_hold_alive(session, ic, now)
    check_version(ic.inspection, ctx.cmd.expected_version)
    ensure_inspection_editable(ic)
    insp = ic.inspection
    if ic.checkout is not None:
        if insp.phase != "before" or ic.checkout.rules_accepted_at is None \
                or data.parking_allowed is not None or data.keys_returned is not None or data.car_locked is not None:
            raise DomainError("INVALID_STATE")
        baseline = ic.vehicle.current_odometer_km
    else:
        assert ic.trip is not None
        if insp.phase != "after":
            raise DomainError("INVALID_STATE")
        baseline = dto.checkout_inspection(session, ic.trip.checkout_attempt_id).odometer_km
    if data.odometer_km is not None and baseline is not None and data.odometer_km < baseline:
        raise DomainError("ODOMETER_ROLLBACK")
    before = dto.inspection_dto(session, insp)
    for field in ("fuel_level", "odometer_km", "new_damage", "cabin_clean", "parking_allowed",
                  "keys_returned", "car_locked"):
        value = getattr(data, field)
        if value is not None:
            setattr(insp, field, value)
    if ic.checkout is not None and data.new_damage is True:
        ic.checkout.no_new_issues = None
    insp.attested_at = None
    touch(insp, now)
    ic.bump_parent(now)
    audit(session, ctx.actor, "inspection.update", "inspection", insp.id,
          before={k: before[k] for k in ctx.cmd.raw_payload}, after=ctx.cmd.raw_payload)
    session.flush()
    return result("inspection.update", dto.inspection_dto(session, insp))


def inspection_confirm_photos(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    ic = load_inspection_context(session, ctx.actor, ctx.target)
    ensure_hold_alive(session, ic, now)
    ensure_inspection_editable(ic)
    check_version(ic.inspection, ctx.cmd.expected_version)
    missing = missing_slots(session, ic.inspection.id)
    if missing:
        raise DomainError("PHOTO_SET_INCOMPLETE", missing_slots=missing)
    ic.inspection.photos_confirmed_at = now
    touch(ic.inspection, now)
    ic.bump_parent(now)
    session.flush()
    return result("inspection.confirm_photos", dto.inspection_dto(session, ic.inspection))


# ------------------------------------------------------------------ возврат

def _lock_trip(ctx: Ctx, trip_id: uuid.UUID, *, owner_only: bool = True
               ) -> tuple[m.Employee, m.Vehicle, m.VehicleAssignment | None, m.Trip]:
    session = ctx.session
    trip = session.get(m.Trip, trip_id)
    if trip is None or (owner_only and trip.employee_id != ctx.actor.id):
        raise DomainError("NOT_FOUND")
    employee = lock_employee(session, trip.employee_id)
    vehicle = lock_vehicle(session, trip.vehicle_id)
    assignment = lock_assignment_for_vehicle(session, trip.vehicle_id)
    trip = lock(session, m.Trip, trip.id)
    return employee, vehicle, assignment, trip


def trip_begin_return(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    _employee, _vehicle, _assignment, trip = _lock_trip(ctx, ctx.target)
    check_version(trip, ctx.cmd.expected_version)
    draft = session.scalar(select(m.ReturnAttempt.id).where(m.ReturnAttempt.trip_id == trip.id,
                                                            m.ReturnAttempt.status == "draft"))
    if trip.status != "active" or draft is not None:
        raise DomainError("INVALID_STATE")
    ret = m.ReturnAttempt(id=uuid.uuid4(), trip_id=trip.id, status="draft", step="math", version=1,
                          created_at=now, updated_at=now)
    session.add(ret)
    session.flush()
    session.add(m.Inspection(id=uuid.uuid4(), phase="after", status="draft", return_attempt_id=ret.id,
                             author_id=trip.employee_id, version=1, created_at=now, updated_at=now))
    trip.status = "returning"
    touch(trip, now)
    audit(session, ctx.actor, "return.begin", "return_attempt", ret.id, after={"trip_id": str(trip.id)})
    session.flush()
    return result("trip.begin_return", dto.return_dto(session, ret))


def _lock_own_return(ctx: Ctx) -> tuple[m.Employee, m.Vehicle, m.VehicleAssignment | None, m.Trip,
                                        m.ReturnAttempt]:
    ret = ctx.session.get(m.ReturnAttempt, ctx.target)
    if ret is None:
        raise DomainError("NOT_FOUND")
    employee, vehicle, assignment, trip = _lock_trip(ctx, ret.trip_id)
    ret = lock(ctx.session, m.ReturnAttempt, ret.id)
    return employee, vehicle, assignment, trip, ret


def _is_current_draft(trip: m.Trip, ret: m.ReturnAttempt) -> bool:
    return ret.status == "draft" and trip.status == "returning" and ret.trip_id == trip.id


def return_cancel(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    _e, _v, _a, trip, ret = _lock_own_return(ctx)
    check_version(ret, ctx.cmd.expected_version)
    if not _is_current_draft(trip, ret):
        raise DomainError("INVALID_STATE")
    ret.status = "cancelled"
    ret.step = "cancelled"
    ret.cancelled_at = now
    touch(ret, now)
    insp = lock(session, m.Inspection, dto.return_inspection(session, ret.id).id)
    insp.status = "abandoned"
    touch(insp, now)
    trip.status = "active"
    touch(trip, now)
    audit(session, ctx.actor, "return.cancel", "return_attempt", ret.id)
    session.flush()
    return result("return.cancel", dto.return_dto(session, ret))


def return_set_location(ctx: Ctx) -> dict[str, Any]:
    session, now, loc = ctx.session, ctx.now, ctx.cmd.payload
    ret = session.get(m.ReturnAttempt, ctx.target)
    if ret is None:
        raise DomainError("NOT_FOUND")
    trip = session.get(m.Trip, ret.trip_id)
    assert trip is not None
    if trip.employee_id != ctx.actor.id and not ctx.actor.is_admin:
        raise DomainError("NOT_FOUND")
    # admin-источник — только администратор; max_geo/manual_map — только сам водитель.
    if (loc.source == "admin" and not ctx.actor.is_admin) or \
            (loc.source != "admin" and trip.employee_id != ctx.actor.id):
        raise DomainError("ACCESS_DENIED")
    _e, vehicle, _a, trip = _lock_trip(ctx, ret.trip_id, owner_only=False)
    ret = lock(session, m.ReturnAttempt, ret.id)
    check_version(ret, ctx.cmd.expected_version)
    if not _is_current_draft(trip, ret) or ret.intent_confirmed_at is None:
        raise DomainError("INVALID_STATE")
    point = m.ParkingLocation(
        id=uuid.uuid4(), vehicle_id=vehicle.id,
        return_attempt_id=None if loc.source == "admin" else ret.id,
        latitude=Decimal(str(round(loc.latitude, 6))), longitude=Decimal(str(round(loc.longitude, 6))),
        source=loc.source, landmark=loc.landmark, author_id=ctx.actor.id, confirmed_at=now, created_at=now)
    session.add(point)
    session.flush()
    ret.parking_location_id = point.id
    touch(ret, now)
    audit(session, ctx.actor, "return.set_location", "return_attempt", ret.id,
          after={"parking_location_id": str(point.id), "source": loc.source})
    session.flush()
    return result("return.set_location", dto.return_dto(session, ret))


def return_complete(ctx: Ctx) -> dict[str, Any]:
    session, now = ctx.session, ctx.now
    employee, vehicle, assignment, trip, ret = _lock_own_return(ctx)
    check_version(ret, ctx.cmd.expected_version)
    if not _is_current_draft(trip, ret) or ret.intent_confirmed_at is None:
        raise DomainError("INVALID_STATE")
    insp = lock(session, m.Inspection, dto.return_inspection(session, ret.id).id)
    missing = missing_slots(session, insp.id)
    if missing:
        raise DomainError("PHOTO_SET_INCOMPLETE", missing_slots=missing)
    answers = (insp.fuel_level, insp.odometer_km, insp.new_damage, insp.cabin_clean,
               insp.parking_allowed, insp.keys_returned, insp.car_locked)
    if insp.status != "draft" or insp.phase != "after" or insp.photos_confirmed_at is None \
            or any(a is None for a in answers):
        raise DomainError("INVALID_STATE")
    if ret.parking_location_id is None:
        raise DomainError("LOCATION_REQUIRED")
    if not (insp.parking_allowed and insp.keys_returned and insp.car_locked):
        raise DomainError("UNSAFE_RETURN")
    before_odo = dto.checkout_inspection(session, trip.checkout_attempt_id).odometer_km
    assert insp.odometer_km is not None
    if before_odo is not None and insp.odometer_km < before_odo:
        raise DomainError("ODOMETER_ROLLBACK")
    if insp.new_damage or not insp.cabin_clean:
        categories = set(session.scalars(select(m.Issue.category).where(
            m.Issue.trip_id == trip.id, m.Issue.stage == "after", m.Issue.inspection_id == insp.id)).all())
        if insp.new_damage and not categories & {"body_damage", "mechanical"} \
                or not insp.cabin_clean and "cleanliness" not in categories:
            raise DomainError("INVALID_STATE")
    if assignment is None or assignment.trip_id != trip.id:
        raise DomainError("INVALID_STATE")
    insp.status = "finalized"
    insp.attested_at = now
    insp.finalized_at = now
    touch(insp, now)
    ret.status = "completed"
    ret.step = "completed"
    ret.completed_at = now
    touch(ret, now)
    trip.status = "completed"
    trip.ended_at = now
    touch(trip, now)
    session.delete(assignment)
    touch(employee, now)
    vehicle.current_parking_location_id = ret.parking_location_id
    vehicle.current_fuel = insp.fuel_level
    vehicle.current_odometer_km = insp.odometer_km
    vehicle.fuel_confirmed_at = now
    vehicle.odometer_confirmed_at = now
    vehicle.last_inspection_id = insp.id
    if insp.new_damage or not insp.cabin_clean:
        vehicle.needs_review = True
    touch(vehicle, now)
    audit(session, ctx.actor, "trip.complete", "trip", trip.id,
          after={"fuel_level": insp.fuel_level, "odometer_km": insp.odometer_km,
                 "needs_review": vehicle.needs_review})
    outbox(session, "trip_completed", "trip", trip.id, trip.version, vehicle_id=vehicle.id, reason=None, now=now)
    session.flush()
    # data-mock возвращает Return; OpenAPI объявляет Trip — см. вопросы контракта в progress/DATA.md.
    return result("return.complete", dto.return_dto(session, ret))


# ------------------------------------------------------------------ замечания

def issue_create(ctx: Ctx) -> dict[str, Any]:
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    if (data.trip_id is None) == (data.inspection_id is None):
        raise DomainError("INVALID_REQUEST")
    if len(set(data.asset_ids)) != len(data.asset_ids):
        raise DomainError("INVALID_REQUEST")
    attempt: m.CheckoutAttempt | None = None
    ret: m.ReturnAttempt | None = None
    trip: m.Trip | None = None
    insp_id = data.inspection_id
    if insp_id is not None:
        insp_row = session.get(m.Inspection, insp_id)
        if insp_row is None:
            raise DomainError("NOT_FOUND")
        if insp_row.checkout_attempt_id is not None:
            attempt = session.get(m.CheckoutAttempt, insp_row.checkout_attempt_id)
        else:
            ret = session.get(m.ReturnAttempt, insp_row.return_attempt_id)
            trip = session.get(m.Trip, ret.trip_id) if ret else None
    else:
        trip = session.get(m.Trip, data.trip_id)
    if attempt is not None:
        if attempt.employee_id != ctx.actor.id or attempt.vehicle_id != ctx.target:
            raise DomainError("NOT_FOUND")
    elif trip is None or trip.employee_id != ctx.actor.id or trip.vehicle_id != ctx.target:
        raise DomainError("NOT_FOUND")
    lock_employee(session, ctx.actor.id)
    vehicle = lock_vehicle(session, ctx.target)
    assignment = lock_assignment_for_vehicle(session, vehicle.id)
    if attempt is not None:
        attempt = lock(session, m.CheckoutAttempt, attempt.id)
        if attempt.status == "holding" and attempt.expires_at <= now:
            expire_attempt(session, attempt, vehicle, assignment, now)
            session.flush()
    if trip is not None:
        trip = lock(session, m.Trip, trip.id)
    if ret is not None:
        ret = lock(session, m.ReturnAttempt, ret.id)
    insp = lock(session, m.Inspection, insp_id) if insp_id else None
    check_version(vehicle, ctx.cmd.expected_version)
    if attempt is not None:
        assert insp is not None
        if attempt.status != "holding" or insp.status != "draft" or assignment is None \
                or assignment.checkout_attempt_id != attempt.id:
            raise DomainError("INVALID_STATE")
    elif trip is not None:
        if trip.status not in ("active", "returning") or assignment is None or assignment.trip_id != trip.id:
            raise DomainError("INVALID_STATE")
        if ret is not None:
            assert insp is not None
            if ret.status != "draft" or insp.status != "draft" or ret.intent_confirmed_at is None \
                    or trip.status != "returning":
                raise DomainError("INVALID_STATE")
    assets: list[m.PhotoAsset] = []
    for asset_id in data.asset_ids:
        asset = session.get(m.PhotoAsset, asset_id, with_for_update=True)
        linked = session.scalar(select(m.IssuePhoto.issue_id).where(m.IssuePhoto.asset_id == asset_id))
        scope_ok = asset is not None and (
            (asset.scope_type == "vehicle" and asset.scope_id == vehicle.id)
            or (asset.scope_type == "trip" and trip is not None and asset.scope_id == trip.id)
            or (asset.scope_type == "inspection" and insp_id is not None and asset.scope_id == insp_id))
        if asset is None or asset.uploaded_by != ctx.actor.id or asset.purpose != "issue" \
                or asset.state != "staged" or linked is not None or not scope_ok \
                or (asset.staging_expires_at is not None and asset.staging_expires_at <= now):
            raise DomainError("NOT_FOUND")
        assets.append(asset)
    stage = "before"
    if trip is not None:
        stage = "after" if ret is not None else ("return" if trip.status == "returning" else "during")
    issue = m.Issue(id=uuid.uuid4(), vehicle_id=vehicle.id, author_id=ctx.actor.id,
                    trip_id=trip.id if trip is not None else None, inspection_id=insp_id, stage=stage,
                    category=data.category, description=data.description, status="open",
                    blocks_issuance=True, version=1, created_at=now, updated_at=now)
    session.add(issue)
    session.flush()
    for ordinal, asset in enumerate(assets, start=1):
        asset.state = "ready"
        asset.staging_expires_at = None
        touch(asset, now)
        session.add(m.IssuePhoto(issue_id=issue.id, ordinal=ordinal, asset_id=asset.id, attached_at=now))
    if attempt is not None:
        assert insp is not None
        attempt.status = "rejected"
        attempt.step = "issue_reported"
        attempt.ended_at = now
        attempt.end_reason = "issue_before_trip"
        touch(attempt, now)
        insp.status = "abandoned"
        touch(insp, now)
        if assignment is not None:
            session.delete(assignment)
    else:
        assert trip is not None
        touch(trip, now)
        if ret is not None:
            touch(ret, now)
    vehicle.needs_review = True
    touch(vehicle, now)
    audit(session, ctx.actor, "issue.create", "issue", issue.id,
          after={"category": data.category, "stage": stage, "vehicle_id": str(vehicle.id)})
    outbox(session, "issue_created", "issue", issue.id, issue.version, vehicle_id=vehicle.id,
           reason=None, now=now)
    session.flush()
    return result("issue.create", dto.issue_dto(session, issue))


def issue_resolve(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    issue = session.get(m.Issue, ctx.target)
    if issue is None:
        raise DomainError("NOT_FOUND")
    lock_vehicle(session, issue.vehicle_id)
    issue = lock(session, m.Issue, issue.id)
    check_version(issue, ctx.cmd.expected_version)
    if issue.status in ("resolved", "known_nonblocking") or (issue.status == data.status):
        raise DomainError("INVALID_STATE")
    before = {"status": issue.status}
    issue.status = data.status
    if data.status == "in_progress":
        issue.blocks_issuance = True
    else:
        issue.blocks_issuance = False
        issue.resolution_comment = data.comment
        issue.resolved_by = ctx.actor.id
        issue.resolved_at = now
    touch(issue, now)
    audit(session, ctx.actor, "issue.resolve", "issue", issue.id, reason=data.comment,
          before=before, after={"status": data.status})
    if data.status != "in_progress":
        outbox(session, "issue_resolved", "issue", issue.id, issue.version, vehicle_id=issue.vehicle_id,
               reason=data.comment, now=now)
    session.flush()
    return result("issue.resolve", dto.issue_dto(session, issue))


# ------------------------------------------------------------------ администрирование

def _admin_intent(ctx: Ctx, operation: str, **fields: Any) -> dict[str, Any]:
    intent = {"operation": operation,
              "target_id": str(ctx.cmd.target_id) if ctx.cmd.target_id else None,
              "expected_version": ctx.cmd.expected_version}
    intent.update(fields)
    return intent


def _cancel_hold_of_vehicle(ctx: Ctx, vehicle: m.Vehicle, reason: str) -> None:
    assignment = lock_assignment_for_vehicle(ctx.session, vehicle.id)
    if assignment is None or assignment.phase != "hold":
        return
    attempt = lock(ctx.session, m.CheckoutAttempt, assignment.checkout_attempt_id)
    attempt.status = "cancelled"
    attempt.ended_at = ctx.now
    attempt.end_reason = reason
    touch(attempt, ctx.now)
    insp = dto.checkout_inspection(ctx.session, attempt.id)
    if insp.status == "draft":
        insp.status = "abandoned"
        insp.updated_at = ctx.now
    ctx.session.delete(assignment)


def vehicle_block(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    vehicle = session.get(m.Vehicle, ctx.target)
    if vehicle is None:
        raise DomainError("NOT_FOUND")
    holder = session.scalar(select(m.VehicleAssignment.employee_id)
                            .where(m.VehicleAssignment.vehicle_id == vehicle.id))
    if holder is not None:
        lock_employee(session, holder)
    vehicle = lock_vehicle(session, vehicle.id)
    check_version(vehicle, ctx.cmd.expected_version)
    consume_admin_challenge(ctx, data.challenge_id, "vehicle_block",
                            _admin_intent(ctx, "vehicle.block", reason=data.reason))
    if vehicle.manual_blocked:
        raise DomainError("INVALID_STATE")
    _cancel_hold_of_vehicle(ctx, vehicle, "vehicle_blocked")
    vehicle.manual_blocked = True
    vehicle.block_reason = data.reason
    vehicle.blocked_by = ctx.actor.id
    touch(vehicle, now)
    audit(session, ctx.actor, "vehicle.block", "vehicle", vehicle.id, reason=data.reason,
          after={"manual_blocked": True})
    outbox(session, "vehicle_blocked", "vehicle", vehicle.id, vehicle.version, vehicle_id=vehicle.id,
           reason=data.reason, now=now)
    session.flush()
    return result("vehicle.block", dto.vehicle_dto(session, vehicle))


def vehicle_unblock(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    vehicle = lock_vehicle(session, ctx.target)
    check_version(vehicle, ctx.cmd.expected_version)
    consume_admin_challenge(ctx, data.challenge_id, "vehicle_unblock",
                            _admin_intent(ctx, "vehicle.unblock", reason=data.reason,
                                          review_completed=True))
    if not vehicle.manual_blocked and not vehicle.needs_review:
        raise DomainError("INVALID_STATE")
    if dto.has_blocking_issue(session, vehicle.id):
        raise DomainError("INVALID_STATE")
    before = {"manual_blocked": vehicle.manual_blocked, "needs_review": vehicle.needs_review}
    vehicle.manual_blocked = False
    vehicle.block_reason = None
    vehicle.blocked_by = None
    vehicle.needs_review = False
    touch(vehicle, now)
    audit(session, ctx.actor, "vehicle.unblock", "vehicle", vehicle.id, reason=data.reason, before=before,
          after={"manual_blocked": False, "needs_review": False})
    session.flush()
    return result("vehicle.unblock", dto.vehicle_dto(session, vehicle))


def vehicle_edit(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    if data.description is None and data.key_instructions is None:
        raise DomainError("INVALID_REQUEST")
    vehicle = lock_vehicle(session, ctx.target)
    check_version(vehicle, ctx.cmd.expected_version)
    before: dict[str, Any] = {}
    after: dict[str, Any] = {}
    for field in ("description", "key_instructions"):
        value = getattr(data, field)
        if value is not None:
            before[field] = getattr(vehicle, field)
            after[field] = value
            setattr(vehicle, field, value)
    touch(vehicle, now)
    audit(session, ctx.actor, "vehicle.edit", "vehicle", vehicle.id, before=before, after=after)
    session.flush()
    return result("vehicle.edit", dto.vehicle_dto(session, vehicle))


def vehicle_correct_snapshot(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    if data.fuel_level is None and data.odometer_km is None and data.location is None:
        raise DomainError("INVALID_REQUEST")
    vehicle = lock_vehicle(session, ctx.target)
    check_version(vehicle, ctx.cmd.expected_version)
    if lock_assignment_for_vehicle(session, vehicle.id) is not None:
        raise DomainError("INVALID_STATE")
    before = {"fuel": vehicle.current_fuel, "odometer_km": vehicle.current_odometer_km,
              "parking_location_id": str(vehicle.current_parking_location_id)
              if vehicle.current_parking_location_id else None}
    if data.fuel_level is not None:
        vehicle.current_fuel = data.fuel_level
        vehicle.fuel_confirmed_at = now
    if data.odometer_km is not None:
        vehicle.current_odometer_km = data.odometer_km
        vehicle.odometer_confirmed_at = now
    if data.location is not None:
        if data.location.source != "admin":
            raise DomainError("INVALID_REQUEST")
        point = m.ParkingLocation(
            id=uuid.uuid4(), vehicle_id=vehicle.id, return_attempt_id=None,
            latitude=Decimal(str(round(data.location.latitude, 6))),
            longitude=Decimal(str(round(data.location.longitude, 6))),
            source="admin", landmark=data.location.landmark, author_id=ctx.actor.id, confirmed_at=now,
            created_at=now)
        session.add(point)
        session.flush()
        vehicle.current_parking_location_id = point.id
    touch(vehicle, now)
    audit(session, ctx.actor, "vehicle.correct_snapshot", "vehicle", vehicle.id, reason=data.reason,
          before=before, after=ctx.cmd.raw_payload)
    session.flush()
    return result("vehicle.correct_snapshot", dto.vehicle_dto(session, vehicle))


def vehicle_annotate(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    vehicle = lock_vehicle(session, ctx.target)
    check_version(vehicle, ctx.cmd.expected_version)
    touch(vehicle, now)
    audit(session, ctx.actor, "vehicle.annotate", "vehicle", vehicle.id, reason=data.reason,
          after={"text": data.text})
    session.flush()
    return result("vehicle.annotate", dto.vehicle_dto(session, vehicle))


def employee_grant(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    consume_admin_challenge(ctx, data.challenge_id, "employee_grant",
                            _admin_intent(ctx, "employee.grant", max_user_id=data.max_user_id,
                                          display_name=data.display_name))
    max_id = int(data.max_user_id)
    if session.scalar(select(m.Employee.id).where(m.Employee.max_user_id == max_id)) is not None:
        raise DomainError("INVALID_STATE")
    emp = m.Employee(id=uuid.uuid4(), max_user_id=max_id, display_name=data.display_name.strip(),
                     role="employee", can_start_trip=True, access_changed_by=ctx.actor.id, version=1,
                     created_at=now, updated_at=now)
    session.add(emp)
    session.flush()
    audit(session, ctx.actor, "employee.grant", "employee", emp.id, after={"role": "employee"})
    outbox(session, "access_changed", "employee", emp.id, emp.version, vehicle_id=None,
           reason="granted", now=now)
    session.flush()
    return result("employee.grant", dto.employee_dto(session, emp))


def employee_access(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    emp = lock_employee(session, ctx.target)
    check_version(emp, ctx.cmd.expected_version)
    consume_admin_challenge(ctx, data.challenge_id, "employee_access",
                            _admin_intent(ctx, "employee.access", can_start_trip=data.can_start_trip,
                                          reason=data.reason))
    if emp.can_start_trip == data.can_start_trip:
        raise DomainError("INVALID_STATE")
    if not data.can_start_trip:
        own = session.scalar(select(m.VehicleAssignment).where(m.VehicleAssignment.employee_id == emp.id))
        if own is not None and own.phase == "hold":
            vehicle = lock_vehicle(session, own.vehicle_id)
            _cancel_hold_of_vehicle(ctx, vehicle, "employee_blocked")
            touch(vehicle, now)
    emp.can_start_trip = data.can_start_trip
    emp.access_block_reason = None if data.can_start_trip else data.reason
    emp.access_changed_by = ctx.actor.id
    touch(emp, now)
    audit(session, ctx.actor, "employee.access", "employee", emp.id, reason=data.reason,
          after={"can_start_trip": data.can_start_trip})
    outbox(session, "access_changed", "employee", emp.id, emp.version, vehicle_id=None,
           reason=data.reason, now=now, extra_recipients=(emp.id,))
    session.flush()
    return result("employee.access", dto.employee_dto(session, emp))


def trip_admin_close(ctx: Ctx) -> dict[str, Any]:
    require_admin(ctx)
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    employee, vehicle, assignment, trip = _lock_trip(ctx, ctx.target, owner_only=False)
    check_version(trip, ctx.cmd.expected_version)
    consume_admin_challenge(ctx, data.challenge_id, "admin_close",
                            _admin_intent(ctx, "trip.admin_close", reason=data.reason))
    if trip.status not in ("active", "returning"):
        raise DomainError("INVALID_STATE")
    available = data.available_data
    ret = session.scalar(select(m.ReturnAttempt).where(m.ReturnAttempt.trip_id == trip.id,
                                                       m.ReturnAttempt.status == "draft").with_for_update())
    if ret is None:
        ret = m.ReturnAttempt(id=uuid.uuid4(), trip_id=trip.id, status="draft", step="admin_close",
                              version=1, created_at=now, updated_at=now)
        session.add(ret)
        session.flush()
        session.add(m.Inspection(id=uuid.uuid4(), phase="after", status="draft", return_attempt_id=ret.id,
                                 author_id=ctx.actor.id, version=1, created_at=now, updated_at=now))
        session.flush()
    insp = lock(session, m.Inspection, dto.return_inspection(session, ret.id).id)
    if available is not None:
        for field in ("fuel_level", "odometer_km", "keys_returned", "car_locked"):
            value = getattr(available, field)
            if value is not None:
                setattr(insp, field, value)
    point_id = None
    if available is not None and (available.latitude is None) != (available.longitude is None):
        raise DomainError("INVALID_REQUEST")
    if available is not None and available.latitude is not None and available.longitude is not None:
        point = m.ParkingLocation(
            id=uuid.uuid4(), vehicle_id=vehicle.id, return_attempt_id=None,
            latitude=Decimal(str(round(available.latitude, 6))),
            longitude=Decimal(str(round(available.longitude, 6))), source="admin",
            landmark=available.landmark, author_id=ctx.actor.id, confirmed_at=now, created_at=now)
        session.add(point)
        session.flush()
        point_id = point.id
    elif ret.parking_location_id is not None:
        point_id = ret.parking_location_id
    missing: list[str] = []
    if len(dto.occupied_slots(session, insp.id)) < 8:
        missing.append("after_photos")
    if insp.fuel_level is None:
        missing.append("fuel_level")
    if insp.odometer_km is None:
        missing.append("odometer_km")
    if point_id is None:
        missing.append("parking_location")
    if insp.keys_returned is None:
        missing.append("keys_returned")
    if insp.car_locked is None:
        missing.append("car_locked")
    insp.status = "abandoned"
    touch(insp, now)
    ret.status = "admin_closed"
    ret.step = "admin_closed"
    ret.parking_location_id = point_id
    ret.completed_at = now
    touch(ret, now)
    trip.status = "closed_by_admin"
    trip.ended_at = now
    trip.closed_by = ctx.actor.id
    trip.close_reason = data.reason
    trip.missing_data = missing
    touch(trip, now)
    if assignment is not None and assignment.trip_id == trip.id:
        session.delete(assignment)
    touch(employee, now)
    if point_id is not None:
        vehicle.current_parking_location_id = point_id
    if insp.fuel_level is not None:
        vehicle.current_fuel = insp.fuel_level
        vehicle.fuel_confirmed_at = now
    if insp.odometer_km is not None and (vehicle.current_odometer_km is None
                                         or insp.odometer_km >= vehicle.current_odometer_km):
        vehicle.current_odometer_km = insp.odometer_km
        vehicle.odometer_confirmed_at = now
    vehicle.needs_review = True
    touch(vehicle, now)
    audit(session, ctx.actor, "trip.admin_close", "trip", trip.id, reason=data.reason,
          after={"missing_data": missing, "available_data": ctx.cmd.raw_payload.get("available_data")})
    outbox(session, "trip_admin_closed", "trip", trip.id, trip.version, vehicle_id=vehicle.id,
           reason=data.reason, now=now, extra_recipients=(trip.employee_id,))
    session.flush()
    return result("trip.admin_close", dto.trip_dto(session, trip))


def conversation_save(ctx: Ctx) -> dict[str, Any]:
    session, now, data = ctx.session, ctx.now, ctx.cmd.payload
    if ctx.target != ctx.actor.id:
        raise DomainError("NOT_FOUND")
    lock_employee(session, ctx.actor.id)
    state = lock(session, m.ConversationState, ctx.actor.id)
    current_version = state.version if state is not None else 1
    if ctx.cmd.expected_version != current_version:
        raise DomainError("STALE_VERSION", current_version=current_version)
    context = dict(dto.EMPTY_CONTEXT)
    context.update(ctx.cmd.raw_payload["context"])
    if state is None:
        state = m.ConversationState(employee_id=ctx.actor.id, flow=data.flow, step=data.step, context=context,
                                    pending_input_kind=data.pending_input_kind, version=2,
                                    created_at=now, updated_at=now)
        session.add(state)
    else:
        state.flow = data.flow
        state.step = data.step
        state.context = context
        state.pending_input_kind = data.pending_input_kind
        touch(state, now)
    session.flush()
    return result("conversation.save", dto.conversation_dto(state))


HANDLERS: dict[str, Callable[[Ctx], dict[str, Any]]] = {
    "checkout.create": checkout_create,
    "checkout.cancel": checkout_cancel,
    "challenge.create": challenge_create,
    "challenge.answer": challenge_answer,
    "checkout.accept_rules": checkout_accept_rules,
    "inspection.update": inspection_update,
    "inspection.confirm_photos": inspection_confirm_photos,
    "checkout.set_no_new_issues": checkout_set_no_new_issues,
    "checkout.start": checkout_start,
    "trip.begin_return": trip_begin_return,
    "return.cancel": return_cancel,
    "return.set_location": return_set_location,
    "return.complete": return_complete,
    "issue.create": issue_create,
    "vehicle.block": vehicle_block,
    "vehicle.unblock": vehicle_unblock,
    "vehicle.edit": vehicle_edit,
    "vehicle.correct_snapshot": vehicle_correct_snapshot,
    "vehicle.annotate": vehicle_annotate,
    "employee.grant": employee_grant,
    "employee.access": employee_access,
    "issue.resolve": issue_resolve,
    "trip.admin_close": trip_admin_close,
    "conversation.save": conversation_save,
}
