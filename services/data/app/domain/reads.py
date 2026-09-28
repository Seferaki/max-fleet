"""Чтения GET /internal/v1/*. Права проверяются на каждом запросе по данным БД."""

from __future__ import annotations

import base64
import hashlib
import json
import uuid
from collections.abc import Sequence
from typing import Any

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from app import models as m
from app.domain import dto
from app.domain.core import Actor
from app.errors import DomainError


def employee_by_max_id(session: Session, max_id: int) -> m.Employee | None:
    return session.scalar(select(m.Employee).where(m.Employee.max_user_id == max_id))


def require_admin_read(actor: Actor) -> None:
    # data-mock и Go-тесты ожидают ACCESS_DENIED для не-администратора.
    if not actor.is_admin:
        raise DomainError("ACCESS_DENIED")


# ------------------------------------------------------------------ пагинация

class Query:
    def __init__(self, items: list[tuple[str, str]], allowed: set[str]) -> None:
        self.values: dict[str, str] = {}
        for key, value in items:
            if key not in allowed or key in self.values:
                raise DomainError("INVALID_REQUEST")
            self.values[key] = value

    def get(self, key: str) -> str | None:
        return self.values.get(key)

    def limit(self) -> int:
        raw = self.values.get("limit")
        if raw is None:
            return 5
        if not raw.isdigit():
            raise DomainError("INVALID_REQUEST")
        value = int(raw)
        if not 1 <= value <= 50:
            raise DomainError("INVALID_REQUEST")
        return value

    def uuid(self, key: str) -> uuid.UUID | None:
        raw = self.values.get(key)
        if raw is None:
            return None
        try:
            if len(raw) != 36:
                raise ValueError
            return uuid.UUID(raw)
        except ValueError as exc:
            raise DomainError("INVALID_REQUEST") from exc


def _binding(parts: Sequence[Any]) -> str:
    return hashlib.sha256(json.dumps([str(p) for p in parts]).encode()).hexdigest()[:16]


def decode_cursor(raw: str | None, binding: Sequence[Any]) -> int:
    if raw is None:
        return 0
    if len(raw) > 2048:
        raise DomainError("INVALID_REQUEST")
    try:
        padded = raw + "=" * (-len(raw) % 4)
        data = json.loads(base64.urlsafe_b64decode(padded.encode()))
        offset, bound = int(data["o"]), str(data["b"])
    except (ValueError, KeyError, TypeError) as exc:
        raise DomainError("INVALID_REQUEST") from exc
    if bound != _binding(binding) or offset < 1:
        raise DomainError("INVALID_REQUEST")
    return offset


def encode_cursor(offset: int, binding: Sequence[Any]) -> str:
    raw = json.dumps({"o": offset, "b": _binding(binding)}, separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def page(items: list[dict[str, Any]], offset: int, limit: int, total: int,
         binding: Sequence[Any]) -> dict[str, Any]:
    end = offset + len(items)
    return {"items": items, "next_cursor": encode_cursor(end, binding) if end < total else None}


# ------------------------------------------------------------------ сотрудник

def get_me(session: Session, max_id: int) -> dict[str, Any]:
    emp = employee_by_max_id(session, max_id)
    return {"allowed": emp is not None, "max_user_id": str(max_id),
            "employee": dto.employee_dto(session, emp) if emp else None}


def get_rules(session: Session) -> dict[str, Any]:
    rules = session.scalar(select(m.RulesVersion).where(m.RulesVersion.is_current.is_(True)))
    if rules is None:
        raise DomainError("NOT_FOUND")
    return {"id": str(rules.id), "version_label": rules.version_label, "body": rules.body}


def get_state(session: Session, actor: Actor) -> dict[str, Any]:
    checkout = session.scalar(select(m.CheckoutAttempt).where(
        m.CheckoutAttempt.employee_id == actor.id, m.CheckoutAttempt.status == "holding"))
    trip = session.scalar(select(m.Trip).where(m.Trip.employee_id == actor.id,
                                               m.Trip.status.in_(("active", "returning"))))
    ret = None
    next_step: str | None = None
    if checkout is not None:
        next_step = checkout.step
    if trip is not None:
        next_step = "active_trip"
        ret = session.scalar(select(m.ReturnAttempt).where(m.ReturnAttempt.trip_id == trip.id,
                                                           m.ReturnAttempt.status == "draft"))
        if ret is not None:
            next_step = ret.step
    conv = session.get(m.ConversationState, actor.id)
    return {
        "checkout": dto.checkout_dto(session, checkout) if checkout else None,
        "trip": dto.trip_dto(session, trip) if trip else None,
        "return": dto.return_dto(session, ret) if ret else None,
        "next_step": next_step,
        "conversation_version": conv.version if conv else 1,
    }


def _trip_visible(trip: m.Trip | None, actor: Actor) -> m.Trip:
    if trip is None or (trip.employee_id != actor.id and not actor.is_admin):
        raise DomainError("NOT_FOUND")
    return trip


def get_checkout(session: Session, actor: Actor, ident: uuid.UUID) -> dict[str, Any]:
    attempt = session.get(m.CheckoutAttempt, ident)
    if attempt is None or (attempt.employee_id != actor.id and not actor.is_admin):
        raise DomainError("NOT_FOUND")
    return dto.checkout_dto(session, attempt)


def get_return(session: Session, actor: Actor, ident: uuid.UUID) -> dict[str, Any]:
    ret = session.get(m.ReturnAttempt, ident)
    if ret is None:
        raise DomainError("NOT_FOUND")
    _trip_visible(session.get(m.Trip, ret.trip_id), actor)
    return dto.return_dto(session, ret)


def get_trip(session: Session, actor: Actor, ident: uuid.UUID) -> dict[str, Any]:
    return dto.trip_dto(session, _trip_visible(session.get(m.Trip, ident), actor))


def get_inspection(session: Session, actor: Actor, ident: uuid.UUID) -> dict[str, Any]:
    insp = session.get(m.Inspection, ident)
    if insp is None:
        raise DomainError("NOT_FOUND")
    if insp.checkout_attempt_id is not None:
        attempt = session.get(m.CheckoutAttempt, insp.checkout_attempt_id)
        owner = attempt.employee_id if attempt else None
    else:
        ret = session.get(m.ReturnAttempt, insp.return_attempt_id)
        trip = session.get(m.Trip, ret.trip_id) if ret else None
        owner = trip.employee_id if trip else None
    if owner != actor.id and not actor.is_admin:
        raise DomainError("NOT_FOUND")
    return dto.inspection_dto(session, insp)


def get_issue(session: Session, actor: Actor, ident: uuid.UUID) -> dict[str, Any]:
    issue = session.get(m.Issue, ident)
    if issue is None or (issue.author_id != actor.id and not actor.is_admin):
        raise DomainError("NOT_FOUND")
    return dto.issue_dto(session, issue)


# ------------------------------------------------------------------ машины

def list_vehicles(session: Session, query: Query) -> dict[str, Any]:
    available = query.get("available")
    if available not in (None, "true", "false"):
        raise DomainError("INVALID_REQUEST")
    limit = query.limit()
    binding = ("vehicles", available, limit)
    offset = decode_cursor(query.get("cursor"), binding)
    vehicles = session.scalars(select(m.Vehicle).order_by(m.Vehicle.plate_normalized, m.Vehicle.id)).all()
    rows = [dto.vehicle_dto(session, v) for v in vehicles]
    if available is not None:
        want = available == "true"
        rows = [r for r in rows if (r["status"] == "available") == want]
    if offset and offset >= len(rows):
        raise DomainError("INVALID_REQUEST")
    return page(rows[offset:offset + limit], offset, limit, len(rows), binding)


def get_vehicle(session: Session, ident: uuid.UUID) -> dict[str, Any]:
    vehicle = session.get(m.Vehicle, ident)
    if vehicle is None:
        raise DomainError("NOT_FOUND")
    return dto.vehicle_dto(session, vehicle)


def latest_after_inspection(session: Session, vehicle_id: uuid.UUID) -> m.Inspection | None:
    return session.scalar(
        select(m.Inspection)
        .join(m.ReturnAttempt, m.ReturnAttempt.id == m.Inspection.return_attempt_id)
        .join(m.Trip, m.Trip.id == m.ReturnAttempt.trip_id)
        .where(m.Trip.vehicle_id == vehicle_id, m.ReturnAttempt.status == "completed",
               m.Inspection.status == "finalized", m.Inspection.phase == "after")
        .order_by(m.Inspection.finalized_at.desc(), m.Inspection.id.desc()).limit(1))


def get_previous_inspection(session: Session, vehicle_id: uuid.UUID) -> dict[str, Any]:
    if session.get(m.Vehicle, vehicle_id) is None:
        raise DomainError("NOT_FOUND")
    insp = latest_after_inspection(session, vehicle_id)
    if insp is None:
        raise DomainError("NOT_FOUND")
    return dto.inspection_dto(session, insp)


# ------------------------------------------------------------------ поездки

def _trip_page(session: Session, stmt: Any, count_stmt: Any, query: Query,
               binding: Sequence[Any]) -> dict[str, Any]:
    limit = query.limit()
    offset = decode_cursor(query.get("cursor"), binding)
    total = session.scalar(count_stmt) or 0
    if offset and offset >= total:
        raise DomainError("INVALID_REQUEST")
    trips = session.scalars(stmt.order_by(m.Trip.started_at.desc(), m.Trip.id.desc())
                            .offset(offset).limit(limit)).all()
    return page([dto.trip_dto(session, t) for t in trips], offset, limit, total, binding)


def list_my_trips(session: Session, actor: Actor, query: Query) -> dict[str, Any]:
    if query.get("scope") != "mine":
        raise DomainError("INVALID_REQUEST")
    cond = m.Trip.employee_id == actor.id
    binding = ("mine", actor.id, query.limit())
    return _trip_page(session, select(m.Trip).where(cond),
                      select(func.count()).select_from(m.Trip).where(cond), query, binding)


def list_admin_trips(session: Session, actor: Actor, query: Query) -> dict[str, Any]:
    state = query.get("state")
    if state not in (None, "active", "returning", "completed", "closed_by_admin"):
        raise DomainError("INVALID_REQUEST")
    employee_id, vehicle_id = query.uuid("employee_id"), query.uuid("vehicle_id")
    limit = query.limit()
    require_admin_read(actor)
    conds = []
    if state:
        conds.append(m.Trip.status == state)
    if employee_id:
        conds.append(m.Trip.employee_id == employee_id)
    if vehicle_id:
        conds.append(m.Trip.vehicle_id == vehicle_id)
    binding = ("admin-trips", state, employee_id, vehicle_id, limit)
    return _trip_page(session, select(m.Trip).where(*conds),
                      select(func.count()).select_from(m.Trip).where(*conds), query, binding)


# ------------------------------------------------------------------ администратор

def admin_summary(session: Session, actor: Actor) -> dict[str, Any]:
    require_admin_read(actor)
    vehicles = session.scalars(select(m.Vehicle)).all()
    available = sum(1 for v in vehicles if dto.vehicle_status(session, v) == "available")
    return {
        "available": available,
        "holding": session.scalar(select(func.count()).select_from(m.CheckoutAttempt)
                                  .where(m.CheckoutAttempt.status == "holding")) or 0,
        "active_trips": session.scalar(select(func.count()).select_from(m.Trip)
                                       .where(m.Trip.status == "active")) or 0,
        "returning": session.scalar(select(func.count()).select_from(m.Trip)
                                    .where(m.Trip.status == "returning")) or 0,
        "needs_review": sum(1 for v in vehicles if v.needs_review),
        "open_issues": session.scalar(select(func.count()).select_from(m.Issue)
                                      .where(m.Issue.status == "open")) or 0,
    }


def list_admin_employees(session: Session, actor: Actor, query: Query) -> dict[str, Any]:
    limit = query.limit()
    require_admin_read(actor)
    binding = ("employees", limit)
    offset = decode_cursor(query.get("cursor"), binding)
    total = session.scalar(select(func.count()).select_from(m.Employee)) or 0
    if offset and offset >= total:
        raise DomainError("INVALID_REQUEST")
    rows = session.scalars(select(m.Employee).order_by(m.Employee.id).offset(offset).limit(limit)).all()
    return page([dto.employee_dto(session, e) for e in rows], offset, limit, total, binding)


def get_admin_employee(session: Session, actor: Actor, ident: uuid.UUID) -> dict[str, Any]:
    require_admin_read(actor)
    emp = session.get(m.Employee, ident)
    if emp is None:
        raise DomainError("NOT_FOUND")
    return dto.employee_dto(session, emp)


def list_admin_issues(session: Session, actor: Actor, query: Query) -> dict[str, Any]:
    status = query.get("status")
    if status not in (None, "open", "in_progress", "resolved", "known_nonblocking"):
        raise DomainError("INVALID_REQUEST")
    vehicle_id = query.uuid("vehicle_id")
    limit = query.limit()
    require_admin_read(actor)
    conds = []
    if status:
        conds.append(m.Issue.status == status)
    if vehicle_id:
        conds.append(m.Issue.vehicle_id == vehicle_id)
    binding = ("issues", status, vehicle_id, limit)
    offset = decode_cursor(query.get("cursor"), binding)
    total = session.scalar(select(func.count()).select_from(m.Issue).where(*conds)) or 0
    if offset and offset >= total:
        raise DomainError("INVALID_REQUEST")
    rows = session.scalars(select(m.Issue).where(*conds)
                           .order_by(m.Issue.updated_at.desc(), m.Issue.id.desc())
                           .offset(offset).limit(limit)).all()
    return page([dto.issue_dto(session, i) for i in rows], offset, limit, total, binding)
