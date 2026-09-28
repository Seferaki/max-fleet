"""Общие примитивы доменных транзакций.

Порядок блокировок (DATABASE §7): employee → vehicle → vehicle_assignment →
checkout_attempt → trip → return_attempt → inspection → прочее.
Внутри одного типа — по возрастанию id.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from app import models as m
from app.errors import DomainError


@dataclass
class Actor:
    employee: m.Employee
    max_id: int
    request_id: uuid.UUID

    @property
    def id(self) -> uuid.UUID:
        return self.employee.id

    @property
    def is_admin(self) -> bool:
        return self.employee.role == "admin"


def db_now(session: Session) -> datetime:
    """Время транзакции по часам БД — единый источник для TTL."""
    return session.execute(select(func.now())).scalar_one()


def lock(session: Session, model: type[Any], ident: Any) -> Any:
    return session.get(model, ident, with_for_update=True, populate_existing=True)


def lock_employee(session: Session, employee_id: uuid.UUID) -> m.Employee:
    emp = lock(session, m.Employee, employee_id)
    if emp is None:
        raise DomainError("NOT_FOUND")
    return emp


def lock_vehicle(session: Session, vehicle_id: uuid.UUID) -> m.Vehicle:
    vehicle = lock(session, m.Vehicle, vehicle_id)
    if vehicle is None:
        raise DomainError("NOT_FOUND")
    return vehicle


def lock_assignment_for_vehicle(session: Session, vehicle_id: uuid.UUID) -> m.VehicleAssignment | None:
    return lock(session, m.VehicleAssignment, vehicle_id)  # type: ignore[no-any-return]


def assignment_of_employee(session: Session, employee_id: uuid.UUID) -> m.VehicleAssignment | None:
    return session.scalar(select(m.VehicleAssignment).where(m.VehicleAssignment.employee_id == employee_id)
                          .with_for_update())


def touch(obj: Any, now: datetime) -> None:
    obj.version = obj.version + 1
    obj.updated_at = now


def check_version(obj: Any, expected: int | None) -> None:
    if expected is not None and obj.version != expected:
        raise DomainError("STALE_VERSION", current_version=obj.version)


def audit(session: Session, actor: Actor | None, action: str, entity_type: str, entity_id: uuid.UUID,
          *, reason: str | None = None, before: dict[str, Any] | None = None,
          after: dict[str, Any] | None = None, request_id: uuid.UUID | None = None) -> None:
    kind = "system" if actor is None else ("admin" if actor.is_admin else "employee")
    session.add(m.AuditLog(
        actor_id=actor.id if actor else None, actor_kind=kind, action=action,
        entity_type=entity_type, entity_id=entity_id, reason=reason,
        before_json=before, after_json=after,
        request_id=request_id or (actor.request_id if actor else uuid.uuid4())))


def outbox(session: Session, event_type: str, aggregate_type: str, aggregate_id: uuid.UUID,
           aggregate_version: int, *, vehicle_id: uuid.UUID | None, reason: str | None,
           now: datetime, extra_recipients: tuple[uuid.UUID, ...] = ()) -> None:
    """Transactional outbox: событие и получатели фиксируются вместе с доменным commit.

    Получатели — все администраторы (как в data-mock) и явно указанные сотрудники
    (например, водитель при аварийном закрытии, сотрудник при смене доступа).
    """
    event = m.OutboxEvent(
        id=uuid.uuid4(), event_type=event_type, aggregate_type=aggregate_type, aggregate_id=aggregate_id,
        aggregate_version=aggregate_version, occurred_at=now, expanded_at=now,
        payload={"resource_id": str(aggregate_id),
                 "vehicle_id": str(vehicle_id) if vehicle_id else None,
                 "reason": reason})
    session.add(event)
    session.flush()
    recipients = list(session.scalars(select(m.Employee.id).where(m.Employee.role == "admin")
                                      .order_by(m.Employee.id)).all())
    for extra in extra_recipients:
        if extra not in recipients:
            recipients.append(extra)
    for recipient in recipients:
        session.add(m.NotificationDelivery(id=uuid.uuid4(), event_id=event.id, recipient_id=recipient,
                                           channel="max", status="pending", attempt_count=0,
                                           next_attempt_at=now, version=1, created_at=now, updated_at=now))


def expire_attempt(session: Session, attempt: m.CheckoutAttempt, vehicle: m.Vehicle,
                   assignment: m.VehicleAssignment | None, now: datetime) -> None:
    """Истечение hold: вызывающий уже держит блокировки в каноническом порядке."""
    attempt.status = "expired"
    attempt.ended_at = now
    attempt.end_reason = "hold_expired"
    touch(attempt, now)
    insp = session.scalar(select(m.Inspection).where(m.Inspection.checkout_attempt_id == attempt.id))
    if insp is not None and insp.status == "draft":
        insp.status = "abandoned"
        insp.updated_at = now
    if assignment is not None and assignment.checkout_attempt_id == attempt.id and assignment.phase == "hold":
        session.delete(assignment)
    touch(vehicle, now)
    audit(session, None, "checkout.expire", "checkout_attempt", attempt.id,
          request_id=uuid.uuid4())


def sweep_expired_holds(session: Session, *, limit: int = 20) -> int:
    """Отдельная короткая транзакция: освободить просроченные hold без ожидания блокировок."""
    now = db_now(session)
    candidates = session.execute(
        select(m.CheckoutAttempt.id, m.CheckoutAttempt.employee_id, m.CheckoutAttempt.vehicle_id)
        .where(m.CheckoutAttempt.status == "holding", m.CheckoutAttempt.expires_at <= now)
        .order_by(m.CheckoutAttempt.expires_at).limit(limit)).all()
    done = 0
    for attempt_id, employee_id, vehicle_id in candidates:
        emp = session.scalar(select(m.Employee).where(m.Employee.id == employee_id)
                             .with_for_update(skip_locked=True))
        if emp is None:
            continue
        vehicle = session.scalar(select(m.Vehicle).where(m.Vehicle.id == vehicle_id)
                                 .with_for_update(skip_locked=True))
        if vehicle is None:
            continue
        assignment = session.scalar(select(m.VehicleAssignment)
                                    .where(m.VehicleAssignment.vehicle_id == vehicle_id)
                                    .with_for_update(skip_locked=True))
        attempt = session.scalar(select(m.CheckoutAttempt).where(m.CheckoutAttempt.id == attempt_id)
                                 .with_for_update(skip_locked=True))
        if attempt is None or attempt.status != "holding" or attempt.expires_at > now:
            continue
        expire_attempt(session, attempt, vehicle, assignment, now)
        done += 1
    session.flush()
    return done


def hold_deadline(now: datetime, minutes: int) -> datetime:
    return now + timedelta(minutes=minutes)
