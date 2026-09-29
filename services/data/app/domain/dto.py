"""Проекции ORM → DTO контракта v1. Приватные поля (object_key, ответ примера) не выходят наружу."""

from __future__ import annotations

import uuid
from datetime import UTC, datetime
from typing import Any

from sqlalchemy import select
from sqlalchemy.orm import Session

from app import models as m

SLOTS = list(range(1, 9))


def ts(value: datetime | None) -> str | None:
    if value is None:
        return None
    return value.astimezone(UTC).isoformat().replace("+00:00", "Z")


def sid(value: uuid.UUID | None) -> str | None:
    return None if value is None else str(value)


def parking_dto(loc: m.ParkingLocation | None) -> dict[str, Any] | None:
    if loc is None:
        return None
    return {
        "id": str(loc.id),
        "latitude": float(loc.latitude),
        "longitude": float(loc.longitude),
        "source": loc.source,
        "landmark": loc.landmark,
        "confirmed_at": ts(loc.confirmed_at),
    }


def employee_dto(session: Session, emp: m.Employee) -> dict[str, Any]:
    active_trip = session.scalar(
        select(m.Trip.id).where(m.Trip.employee_id == emp.id, m.Trip.status.in_(("active", "returning"))))
    return {
        "id": str(emp.id),
        "max_user_id": str(emp.max_user_id),
        "display_name": emp.display_name,
        "role": emp.role,
        "can_start_trip": emp.can_start_trip,
        "active_trip_id": sid(active_trip),
        "version": emp.version,
        "updated_at": ts(emp.updated_at),
    }


def vehicle_status(session: Session, vehicle: m.Vehicle) -> str:
    phase = session.scalar(select(m.VehicleAssignment.phase).where(m.VehicleAssignment.vehicle_id == vehicle.id))
    if phase == "trip":
        return "in_trip"
    if phase == "hold":
        return "holding"
    if not vehicle_issuable(session, vehicle):
        return "unavailable"
    return "available"


def has_blocking_issue(session: Session, vehicle_id: uuid.UUID) -> bool:
    return session.scalar(select(m.Issue.id).where(
        m.Issue.vehicle_id == vehicle_id, m.Issue.blocks_issuance.is_(True)).limit(1)) is not None


def vehicle_issuable(session: Session, vehicle: m.Vehicle) -> bool:
    """Карточка готова к выдаче без учёта текущего закрепления."""
    return (not vehicle.manual_blocked and not vehicle.needs_review
            and vehicle.current_parking_location_id is not None
            and vehicle.key_instructions.strip() != ""
            and not has_blocking_issue(session, vehicle.id))


def vehicle_dto(session: Session, vehicle: m.Vehicle) -> dict[str, Any]:
    parking = session.get(m.ParkingLocation, vehicle.current_parking_location_id) \
        if vehicle.current_parking_location_id else None
    known = session.scalars(select(m.Issue.id).where(
        m.Issue.vehicle_id == vehicle.id, m.Issue.status == "known_nonblocking")
        .order_by(m.Issue.created_at, m.Issue.id)).all()
    return {
        "id": str(vehicle.id),
        "plate": vehicle.plate,
        "make": vehicle.make,
        "model": vehicle.model,
        "description": vehicle.description,
        "key_instructions": vehicle.key_instructions,
        "status": vehicle_status(session, vehicle),
        "manual_blocked": vehicle.manual_blocked,
        "needs_review": vehicle.needs_review,
        "current_parking": parking_dto(parking),
        "current_fuel": vehicle.current_fuel,
        "current_odometer_km": vehicle.current_odometer_km,
        "fuel_confirmed_at": ts(vehicle.fuel_confirmed_at),
        "odometer_confirmed_at": ts(vehicle.odometer_confirmed_at),
        "known_nonblocking_issues": [str(i) for i in known],
        "version": vehicle.version,
        "updated_at": ts(vehicle.updated_at),
    }


def occupied_slots(session: Session, inspection_id: uuid.UUID) -> list[int]:
    return sorted(session.scalars(select(m.InspectionPhoto.slot)
                                  .where(m.InspectionPhoto.inspection_id == inspection_id)).all())


def inspection_dto(session: Session, insp: m.Inspection) -> dict[str, Any]:
    occupied = occupied_slots(session, insp.id)
    return {
        "id": str(insp.id),
        "phase": insp.phase,
        "status": insp.status,
        "fuel_level": insp.fuel_level,
        "odometer_km": insp.odometer_km,
        "new_damage": insp.new_damage,
        "cabin_clean": insp.cabin_clean,
        "parking_allowed": insp.parking_allowed,
        "keys_returned": insp.keys_returned,
        "car_locked": insp.car_locked,
        "occupied_slots": occupied,
        "missing_slots": [s for s in SLOTS if s not in occupied],
        "photos_confirmed_at": ts(insp.photos_confirmed_at),
        "version": insp.version,
        "updated_at": ts(insp.updated_at),
    }


def checkout_inspection(session: Session, attempt_id: uuid.UUID) -> m.Inspection:
    insp = session.scalar(select(m.Inspection).where(m.Inspection.checkout_attempt_id == attempt_id))
    assert insp is not None
    return insp


def return_inspection(session: Session, return_id: uuid.UUID) -> m.Inspection:
    insp = session.scalar(select(m.Inspection).where(m.Inspection.return_attempt_id == return_id))
    assert insp is not None
    return insp


def checkout_dto(session: Session, attempt: m.CheckoutAttempt) -> dict[str, Any]:
    return {
        "id": str(attempt.id),
        "vehicle_id": str(attempt.vehicle_id),
        "employee_id": str(attempt.employee_id),
        "status": attempt.status,
        "step": attempt.step,
        "expires_at": ts(attempt.expires_at),
        "intent_confirmed_at": ts(attempt.intent_confirmed_at),
        "rules_version_id": sid(attempt.rules_version_id),
        "rules_accepted_at": ts(attempt.rules_accepted_at),
        "no_new_issues": attempt.no_new_issues,
        "inspection": inspection_dto(session, checkout_inspection(session, attempt.id)),
        "version": attempt.version,
        "updated_at": ts(attempt.updated_at),
    }


def return_dto(session: Session, ret: m.ReturnAttempt) -> dict[str, Any]:
    parking = session.get(m.ParkingLocation, ret.parking_location_id) if ret.parking_location_id else None
    return {
        "id": str(ret.id),
        "trip_id": str(ret.trip_id),
        "status": ret.status,
        "step": ret.step,
        "intent_confirmed_at": ts(ret.intent_confirmed_at),
        "parking_location": parking_dto(parking),
        "inspection": inspection_dto(session, return_inspection(session, ret.id)),
        "version": ret.version,
        "updated_at": ts(ret.updated_at),
    }


def issue_dto(session: Session, issue: m.Issue) -> dict[str, Any]:
    assets = session.scalars(select(m.IssuePhoto.asset_id).where(m.IssuePhoto.issue_id == issue.id)
                             .order_by(m.IssuePhoto.ordinal)).all()
    return {
        "id": str(issue.id),
        "vehicle_id": str(issue.vehicle_id),
        "author_id": str(issue.author_id),
        "assigned_to": sid(issue.assigned_to),
        "stage": issue.stage,
        "category": issue.category,
        "description": issue.description,
        "status": issue.status,
        "blocks_issuance": issue.blocks_issuance,
        "resolution_comment": issue.resolution_comment,
        "resolved_by": sid(issue.resolved_by),
        "resolved_at": ts(issue.resolved_at),
        "trip_id": sid(issue.trip_id),
        "inspection_id": sid(issue.inspection_id),
        "asset_ids": [str(a) for a in assets],
        "version": issue.version,
        "updated_at": ts(issue.updated_at),
    }


def trip_current_return(session: Session, trip: m.Trip) -> m.ReturnAttempt | None:
    """Черновик либо итоговый возврат; отменённые черновики не показываются."""
    return session.scalar(select(m.ReturnAttempt).where(
        m.ReturnAttempt.trip_id == trip.id,
        m.ReturnAttempt.status.in_(("draft", "completed", "admin_closed")))
        .order_by(m.ReturnAttempt.created_at.desc(), m.ReturnAttempt.id.desc()).limit(1))


def trip_dto(session: Session, trip: m.Trip) -> dict[str, Any]:
    before = checkout_inspection(session, trip.checkout_attempt_id)
    ret = trip_current_return(session, trip)
    after = None
    parking = None
    if ret is not None and ret.status in ("completed", "admin_closed"):
        after_insp = return_inspection(session, ret.id)
        if after_insp.status == "finalized":
            after = inspection_dto(session, after_insp)
        if ret.parking_location_id:
            parking = parking_dto(session.get(m.ParkingLocation, ret.parking_location_id))
    issues = session.scalars(select(m.Issue).where(m.Issue.trip_id == trip.id)
                             .order_by(m.Issue.created_at, m.Issue.id)).all()
    return {
        "id": str(trip.id),
        "vehicle_id": str(trip.vehicle_id),
        "employee_id": str(trip.employee_id),
        "checkout_id": str(trip.checkout_attempt_id),
        "status": trip.status,
        "started_at": ts(trip.started_at),
        "ended_at": ts(trip.ended_at),
        "return_id": sid(ret.id) if ret is not None else None,
        "missing_data": list(trip.missing_data or []),
        "before_inspection": inspection_dto(session, before),
        "after_inspection": after,
        "parking_location": parking,
        "issues": [issue_dto(session, i) for i in issues],
        "version": trip.version,
        "updated_at": ts(trip.updated_at),
    }


def challenge_dto(ch: m.Challenge) -> dict[str, Any]:
    return {
        "id": str(ch.id),
        "purpose": ch.purpose,
        "question": f"{ch.operand_a} + {ch.operand_b} = ?",
        "options": list(ch.options),
        "expires_at": ts(ch.expires_at),
        "attempts_remaining": 3 - ch.wrong_attempts,
        "version": ch.version,
        "updated_at": ts(ch.updated_at),
    }


EMPTY_CONTEXT: dict[str, Any] = {k: None for k in (
    "target_id", "selected_slot", "challenge_id", "vehicle_id", "trip_id", "return_id", "issue_id",
    "issue_version", "trip_version", "cursor", "draft_text", "issue_category", "vehicle_version",
    "correction_odometer_km", "admin_close_data", "challenge_version", "challenge_question",
    "challenge_expires_at")}
EMPTY_CONTEXT["asset_ids"] = []
EMPTY_CONTEXT["challenge_options"] = []


def conversation_dto(state: m.ConversationState) -> dict[str, Any]:
    return {
        "flow": state.flow,
        "step": state.step,
        "context": state.context,
        "pending_input_kind": state.pending_input_kind,
        "version": state.version,
        "updated_at": ts(state.updated_at),
    }
