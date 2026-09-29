"""Идемпотентное выполнение команд: домен + audit + outbox + результат — одна транзакция."""

from __future__ import annotations

import json
import uuid
from collections.abc import Callable
from datetime import timedelta
from typing import Any, TypeVar

from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert
from sqlalchemy.exc import DBAPIError, IntegrityError, OperationalError
from sqlalchemy.orm import Session, sessionmaker

from app import models as m
from app.api import schemas as s
from app.config import Settings
from app.db import run_transaction, sqlstate
from app.domain.commands import HANDLERS, Ctx
from app.domain.core import Actor, db_now, sweep_expired_holds
from app.errors import DomainError

T = TypeVar("T")
IDEMPOTENCY_TTL = timedelta(days=7)

CONSTRAINT_ERRORS = {
    "pk_vehicle_assignments": "VEHICLE_UNAVAILABLE",
    "uq_vehicle_assignments_employee_id": "USER_BUSY",
    "uq_trips_open_vehicle": "VEHICLE_UNAVAILABLE",
    "uq_trips_open_employee": "USER_BUSY",
    "uq_idempotency_records_scope_key": "COMMAND_IN_PROGRESS",
    "uq_inspection_photos_sha": "DUPLICATE_PHOTO",
    "uq_inspection_photos_event": "DUPLICATE_PHOTO",
    "uq_employees_max_user_id": "INVALID_STATE",
}


def translate_db_error(exc: DBAPIError) -> DomainError:
    if isinstance(exc, IntegrityError):
        name = getattr(getattr(exc.orig, "diag", None), "constraint_name", None) or ""
        return DomainError(CONSTRAINT_ERRORS.get(name, "INVALID_STATE"))
    state = sqlstate(exc)
    if state in ("55P03", "57014", "40001", "40P01"):
        return DomainError("TEMPORARY_FAILURE")
    if isinstance(exc, OperationalError):
        return DomainError("DATABASE_UNAVAILABLE")
    return DomainError("TEMPORARY_FAILURE")


def guarded(factory: sessionmaker[Session], work: Callable[[Session], T]) -> T:
    try:
        return run_transaction(factory, work)
    except DBAPIError as exc:
        raise translate_db_error(exc) from exc


def sweep(factory: sessionmaker[Session]) -> None:
    """Освободить просроченные hold в отдельной короткой транзакции (без ожидания блокировок)."""
    try:
        run_transaction(factory, sweep_expired_holds, attempts=1)
    except DBAPIError as exc:
        raise translate_db_error(exc) from exc


def load_actor(session: Session, max_id: int, request_id: str) -> Actor:
    emp = session.scalar(select(m.Employee).where(m.Employee.max_user_id == max_id))
    if emp is None:
        raise DomainError("ACCESS_DENIED")
    return Actor(employee=emp, max_id=max_id, request_id=uuid.UUID(request_id))


def check_inbox_lease(session: Session, event_id: str | None, lease: str | None, max_id: int) -> None:
    if event_id is None and lease is None:
        return
    if event_id is None or lease is None:
        raise DomainError("INVALID_REQUEST")
    try:
        ident = uuid.UUID(event_id)
    except ValueError as exc:
        raise DomainError("INVALID_REQUEST") from exc
    event = session.scalar(select(m.InboundEvent).where(m.InboundEvent.id == ident).with_for_update())
    now = db_now(session)
    if event is None or event.lease_token != lease or event.status != "processing" \
            or event.lease_until is None or event.lease_until <= now or event.actor_max_user_id != max_id:
        raise DomainError("LEASE_EXPIRED")


def claim_idempotency(session: Session, scope: str, key: str, request_sha: str, actor: Actor | None,
                      operation: str) -> dict[str, Any] | None:
    """None — ключ новый и захвачен; иначе сохранённый результат."""
    now = db_now(session)
    inserted = session.execute(
        insert(m.IdempotencyRecord)
        .values(id=uuid.uuid4(), scope=scope, key=key, request_sha256=request_sha,
                actor_id=actor.id if actor else None, operation=operation, status="processing",
                expires_at=now + IDEMPOTENCY_TTL, created_at=now, updated_at=now, version=1)
        .on_conflict_do_nothing(index_elements=["scope", "key"])
        .returning(m.IdempotencyRecord.id)).scalar_one_or_none()
    if inserted is not None:
        return None
    record = session.scalar(select(m.IdempotencyRecord).where(
        m.IdempotencyRecord.scope == scope, m.IdempotencyRecord.key == key).with_for_update())
    assert record is not None
    if record.request_sha256 != request_sha:
        raise DomainError("IDEMPOTENCY_CONFLICT")
    if record.status != "completed" or record.response_json is None:
        raise DomainError("COMMAND_IN_PROGRESS")
    return record.response_json


def complete_idempotency(session: Session, scope: str, key: str, response: dict[str, Any],
                         resource_type: str | None = None, resource_id: str | None = None) -> None:
    record = session.scalar(select(m.IdempotencyRecord).where(
        m.IdempotencyRecord.scope == scope, m.IdempotencyRecord.key == key))
    assert record is not None
    record.status = "completed"
    record.http_status = 200
    record.response_json = response
    record.resource_type = resource_type
    record.resource_id = uuid.UUID(resource_id) if resource_id else None
    record.updated_at = db_now(session)
    record.version += 1


def command_scope(max_id: int) -> str:
    return f"command:{max_id}"


def execute_command(factory: sessionmaker[Session], settings: Settings, *, max_id: int, request_id: str,
                    key: str, body: bytes, inbox_event_id: str | None,
                    inbox_lease: str | None) -> dict[str, Any]:
    cmd = s.parse_command(body)
    request_sha = s.canonical_sha(json.loads(body))
    scope = command_scope(max_id)
    sweep(factory)

    def work(session: Session) -> dict[str, Any]:
        actor = load_actor(session, max_id, request_id)
        check_inbox_lease(session, inbox_event_id, inbox_lease, max_id)
        saved = claim_idempotency(session, scope, key, request_sha, actor, cmd.operation)
        if saved is not None:
            return saved
        ctx = Ctx(session, actor, cmd, db_now(session), settings)
        response = HANDLERS[cmd.operation](ctx)
        aggregate = response["aggregate"]
        complete_idempotency(session, scope, key, response, cmd.operation,
                             aggregate.get("id") if isinstance(aggregate, dict) else None)
        return response

    return guarded(factory, work)


def get_command_result(session: Session, max_id: int, key: str, operation: str) -> dict[str, Any]:
    load_actor(session, max_id, str(uuid.uuid4()))
    record = session.scalar(select(m.IdempotencyRecord).where(
        m.IdempotencyRecord.scope == command_scope(max_id), m.IdempotencyRecord.key == key))
    if record is None or record.operation != operation or record.status != "completed" \
            or record.response_json is None:
        raise DomainError("NOT_FOUND")
    return record.response_json
