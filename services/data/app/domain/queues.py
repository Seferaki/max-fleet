"""Технические очереди worker: inbox, lease poller-а, уведомления.

Семантика совпадает с Go data-mock: lease 2 минуты, до 5 попыток, по одному
активному событию на actor/получателя, повтор idempotency-ключа возвращает
прежний результат, пока аренда действительна.
"""

from __future__ import annotations

import json
import secrets
import uuid
from datetime import datetime, timedelta
from typing import Any

from pydantic import ValidationError
from sqlalchemy import select, text
from sqlalchemy.orm import Session

from app import models as m
from app.api.schemas import canonical_sha
from app.api.worker_schemas import (
    IntegrationCheckpointIn,
    IntegrationLeaseIn,
    NormalizedEvent,
    NotificationAckIn,
    NotificationRetryIn,
    QueueAckIn,
    QueueClaimIn,
    QueueRetryIn,
)
from app.domain.core import db_now, touch
from app.domain.dto import ts
from app.domain.executor import claim_idempotency, complete_idempotency
from app.errors import DomainError

LEASE = timedelta(minutes=2)
MAX_ATTEMPTS = 5
CLAIM_LOCK_INBOX = 7_331_101
CLAIM_LOCK_NOTIFY = 7_331_102


def parse(model: Any, body: bytes) -> Any:
    try:
        return model.model_validate_json(body, strict=True)
    except ValidationError as exc:
        raise DomainError("INVALID_REQUEST") from exc


def _scope(route: str) -> str:
    return f"worker:{route}"


def _token() -> str:
    return secrets.token_urlsafe(32)


# ------------------------------------------------------------------ inbox

def store_inbox(session: Session, key: str, body: bytes) -> dict[str, Any]:
    event = parse(NormalizedEvent, body)
    event.check_semantics()
    raw = json.loads(body)
    signature = canonical_sha(raw)
    identity = f"{event.integration_key}\x00{event.event_key}"
    saved = claim_idempotency(session, _scope("inbox"), key, canonical_sha([identity, signature]), None,
                              "inbox.store")
    if saved is not None:
        return saved
    if session.get(m.IntegrationState, event.integration_key) is None:
        raise DomainError("NOT_FOUND")
    now = db_now(session)
    existing = session.scalar(select(m.InboundEvent).where(
        m.InboundEvent.integration_key == event.integration_key, m.InboundEvent.event_key == event.event_key))
    if existing is not None:
        if canonical_sha(existing.payload) != signature:
            raise DomainError("IDEMPOTENCY_CONFLICT")
        response = {"id": str(existing.id), "duplicate": True, "stored_at": ts(existing.received_at)}
        complete_idempotency(session, _scope("inbox"), key, response)
        return response
    actor = session.scalar(select(m.Employee).where(m.Employee.max_user_id == int(event.actor_max_user_id)))
    row = m.InboundEvent(
        id=uuid.uuid4(), integration_key=event.integration_key, event_key=event.event_key,
        event_type=event.event_type, actor_id=actor.id if actor else None,
        actor_max_user_id=int(event.actor_max_user_id), payload=raw, payload_schema_version=1,
        received_at=now, status="pending", attempt_count=0, next_attempt_at=now, version=1,
        created_at=now, updated_at=now)
    session.add(row)
    if actor is not None and event.event_type == "bot_started":
        actor.max_chat_id = int(event.chat_id)
        actor.bot_started_at = now
    session.flush()
    response = {"id": str(row.id), "duplicate": False, "stored_at": ts(now)}
    complete_idempotency(session, _scope("inbox"), key, response)
    return response


def _inbox_lease_view(row: m.InboundEvent) -> dict[str, Any]:
    return {"id": str(row.id), "event": row.payload, "lease_token": row.lease_token,
            "lease_expires_at": ts(row.lease_until), "attempt": row.attempt_count}


def claim_inbox(session: Session, key: str, body: bytes) -> dict[str, Any]:
    req = parse(QueueClaimIn, body)
    scope = _scope("inbox/claim")
    saved = claim_idempotency(session, scope, key, canonical_sha(json.loads(body)), None, "inbox.claim")
    now = db_now(session)
    if saved is not None:
        for item in saved["items"]:
            row = session.get(m.InboundEvent, uuid.UUID(item["id"]))
            if row is None or row.status != "processing" or row.lease_token != item["lease_token"] \
                    or row.lease_until is None or row.lease_until <= now:
                raise DomainError("LEASE_EXPIRED")
        return saved
    session.execute(text("SELECT pg_advisory_xact_lock(:id)"), {"id": CLAIM_LOCK_INBOX})
    rows = session.scalars(select(m.InboundEvent)
                           .where(m.InboundEvent.status.in_(("pending", "processing", "retry")))
                           .order_by(m.InboundEvent.sequence).limit(500).with_for_update()).all()
    seen: set[int] = set()
    items: list[dict[str, Any]] = []
    for row in rows:
        if len(items) >= req.max_items:
            break
        if row.actor_max_user_id in seen:
            continue
        seen.add(row.actor_max_user_id)
        leased = row.status == "processing" and row.lease_until is not None and row.lease_until > now
        if leased or row.next_attempt_at > now:
            continue
        row.status = "processing"
        row.lease_owner = req.worker_id
        row.lease_token = _token()
        row.lease_until = now + LEASE
        row.attempt_count += 1
        row.next_attempt_at = now
        touch(row, now)
        items.append(_inbox_lease_view(row))
    response = {"items": items}
    complete_idempotency(session, scope, key, response)
    return response


def _transition_inbox(session: Session, key: str, event_id: uuid.UUID, body: bytes, retry: bool
                      ) -> dict[str, Any]:
    req = parse(QueueRetryIn if retry else QueueAckIn, body)
    route = f"inbox/{event_id}/{'retry' if retry else 'ack'}"
    saved = claim_idempotency(session, _scope(route), key, canonical_sha(json.loads(body)), None, route)
    if saved is not None:
        return saved
    row = session.get(m.InboundEvent, event_id, with_for_update=True)
    if row is None:
        raise DomainError("NOT_FOUND")
    now = db_now(session)
    if row.status != "processing" or row.lease_token is None or row.lease_until is None \
            or row.lease_until <= now or not secrets.compare_digest(row.lease_token, req.lease_token):
        raise DomainError("LEASE_EXPIRED")
    row.lease_token = None
    row.lease_owner = None
    row.lease_until = None
    state = "done"
    if retry:
        state = "retry"
        row.last_error_code = req.error_code
        row.next_attempt_at = req.next_attempt_at
        if row.attempt_count >= MAX_ATTEMPTS:
            state = "dead"
    row.status = state
    touch(row, now)
    response = {"id": str(row.id), "state": state, "updated_at": ts(now)}
    complete_idempotency(session, _scope(route), key, response)
    return response


def ack_inbox(session: Session, key: str, event_id: uuid.UUID, body: bytes) -> dict[str, Any]:
    return _transition_inbox(session, key, event_id, body, retry=False)


def retry_inbox(session: Session, key: str, event_id: uuid.UUID, body: bytes) -> dict[str, Any]:
    return _transition_inbox(session, key, event_id, body, retry=True)


# ------------------------------------------------------------------ интеграция

def _integration_view(row: m.IntegrationState, now: datetime) -> dict[str, Any]:
    active = row.poller_lease_until is not None
    return {"key": row.integration_key, "mode": row.mode, "marker": row.poll_marker,
            "lease_expires_at": ts(row.poller_lease_until) if active else None,
            "version": row.version, "updated_at": ts(row.updated_at)}


def get_integration(session: Session, integration_key: str) -> dict[str, Any]:
    row = session.get(m.IntegrationState, integration_key)
    if row is None:
        raise DomainError("NOT_FOUND")
    return _integration_view(row, db_now(session))


def lease_integration(session: Session, key: str, integration_key: str, body: bytes) -> dict[str, Any]:
    req = parse(IntegrationLeaseIn, body)
    route = f"integrations/{integration_key}/lease"
    saved = claim_idempotency(session, _scope(route), key, canonical_sha(json.loads(body)), None, route)
    row = session.get(m.IntegrationState, integration_key, with_for_update=True)
    if row is None:
        raise DomainError("NOT_FOUND")
    now = db_now(session)
    if saved is not None:
        if row.poller_lease_token != saved["lease_token"] or row.poller_lease_until is None \
                or row.poller_lease_until <= now:
            raise DomainError("LEASE_EXPIRED")
        return saved
    if row.version != req.expected_version:
        raise DomainError("STALE_VERSION", current_version=row.version)
    active = row.poller_lease_until is not None and row.poller_lease_until > now
    if active and row.poller_lease_owner != req.worker_id:
        raise DomainError("COMMAND_IN_PROGRESS")
    if not active or row.poller_lease_token is None:
        row.poller_lease_token = _token()
    row.poller_lease_owner = req.worker_id
    row.poller_lease_until = now + LEASE
    touch(row, now)
    session.flush()
    response = {"lease_token": row.poller_lease_token, "lease_expires_at": ts(row.poller_lease_until),
                "integration": _integration_view(row, now)}
    complete_idempotency(session, _scope(route), key, response)
    return response


def checkpoint_integration(session: Session, key: str, integration_key: str, body: bytes) -> dict[str, Any]:
    req = parse(IntegrationCheckpointIn, body)
    if len(set(req.stored_event_ids)) != len(req.stored_event_ids):
        raise DomainError("INVALID_REQUEST")
    route = f"integrations/{integration_key}/checkpoint"
    saved = claim_idempotency(session, _scope(route), key, canonical_sha(json.loads(body)), None, route)
    row = session.get(m.IntegrationState, integration_key, with_for_update=True)
    if row is None:
        raise DomainError("NOT_FOUND")
    now = db_now(session)
    lease_ok = (row.poller_lease_token is not None and row.poller_lease_until is not None
                and row.poller_lease_until > now
                and secrets.compare_digest(row.poller_lease_token, req.lease_token))
    if saved is not None:
        if not lease_ok:
            raise DomainError("LEASE_EXPIRED")
        return saved
    if not lease_ok:
        raise DomainError("LEASE_EXPIRED")
    if row.version != req.expected_version or row.poll_marker != req.previous_marker:
        raise DomainError("STALE_VERSION", current_version=row.version)
    for event_id in req.stored_event_ids:
        stored = session.get(m.InboundEvent, event_id)
        if stored is None or stored.integration_key != integration_key:
            raise DomainError("COMMAND_IN_PROGRESS")
    row.poll_marker = req.new_marker
    touch(row, now)
    session.flush()
    response = _integration_view(row, now)
    complete_idempotency(session, _scope(route), key, response)
    return response


# ------------------------------------------------------------------ уведомления

def _notification_view(session: Session, row: m.NotificationDelivery) -> dict[str, Any]:
    event = session.get(m.OutboxEvent, row.event_id)
    recipient = session.get(m.Employee, row.recipient_id)
    assert event is not None and recipient is not None
    return {
        "delivery_id": str(row.id),
        "event": {"type": event.event_type, "resource_id": event.payload["resource_id"],
                  "vehicle_id": event.payload.get("vehicle_id"), "reason": event.payload.get("reason"),
                  "occurred_at": ts(event.occurred_at)},
        "recipient_max_user_id": str(recipient.max_user_id),
        "enqueued_at": ts(row.created_at),
        "lease_token": row.lease_token,
        "lease_expires_at": ts(row.lease_until),
        "attempt": row.attempt_count,
    }


def claim_notifications(session: Session, key: str, body: bytes) -> dict[str, Any]:
    req = parse(QueueClaimIn, body)
    scope = _scope("notifications/claim")
    saved = claim_idempotency(session, scope, key, canonical_sha(json.loads(body)), None, "notifications.claim")
    now = db_now(session)
    if saved is not None:
        for item in saved["items"]:
            row = session.get(m.NotificationDelivery, uuid.UUID(item["delivery_id"]))
            if row is None or row.status != "sending" or row.lease_token != item["lease_token"] \
                    or row.lease_until is None or row.lease_until <= now:
                raise DomainError("LEASE_EXPIRED")
        return saved
    session.execute(text("SELECT pg_advisory_xact_lock(:id)"), {"id": CLAIM_LOCK_NOTIFY})
    rows = session.scalars(select(m.NotificationDelivery)
                           .join(m.OutboxEvent, m.OutboxEvent.id == m.NotificationDelivery.event_id)
                           .where(m.NotificationDelivery.status.in_(("pending", "sending", "retry")))
                           .order_by(m.OutboxEvent.occurred_at, m.NotificationDelivery.created_at,
                                     m.NotificationDelivery.id)
                           .limit(500).with_for_update(of=m.NotificationDelivery)).all()
    blocked: set[uuid.UUID] = set()
    items: list[dict[str, Any]] = []
    for row in rows:
        if row.recipient_id in blocked:
            continue
        blocked.add(row.recipient_id)
        leased = row.status == "sending" and row.lease_until is not None and row.lease_until > now
        if leased or row.next_attempt_at > now:
            continue
        row.status = "sending"
        row.lease_owner = req.worker_id
        row.lease_token = _token()
        row.lease_until = now + LEASE
        row.attempt_count += 1
        row.next_attempt_at = now
        touch(row, now)
        items.append(_notification_view(session, row))
        if len(items) >= req.max_items:
            break
    response = {"items": items}
    complete_idempotency(session, scope, key, response)
    return response


def _transition_notification(session: Session, key: str, delivery_id: uuid.UUID, body: bytes,
                             retry: bool) -> dict[str, Any]:
    req = parse(NotificationRetryIn if retry else NotificationAckIn, body)
    route = f"notifications/{delivery_id}/{'retry' if retry else 'ack'}"
    saved = claim_idempotency(session, _scope(route), key, canonical_sha(json.loads(body)), None, route)
    if saved is not None:
        return saved
    row = session.get(m.NotificationDelivery, delivery_id, with_for_update=True)
    if row is None:
        raise DomainError("NOT_FOUND")
    now = db_now(session)
    if row.status != "sending" or row.lease_token is None or row.lease_until is None \
            or row.lease_until <= now or not secrets.compare_digest(row.lease_token, req.lease_token):
        raise DomainError("LEASE_EXPIRED")
    if retry and ((req.dead and req.retry_after is not None)
                  or (not req.dead and (req.retry_after is None or req.retry_after <= now))):
        raise DomainError("INVALID_REQUEST")
    row.lease_token = None
    row.lease_owner = None
    row.lease_until = None
    if retry:
        row.last_error_code = req.error_code
        row.provider_message_id = None
        state = "retry"
        if req.dead or row.attempt_count >= MAX_ATTEMPTS:
            state = "dead"
        else:
            row.next_attempt_at = req.retry_after
    else:
        state = "sent"
        row.provider_message_id = req.provider_message_id
        row.last_error_code = None
        row.sent_at = now
    row.status = state
    touch(row, now)
    response = {"id": str(row.id), "state": state, "updated_at": ts(now)}
    complete_idempotency(session, _scope(route), key, response)
    return response


def ack_notification(session: Session, key: str, delivery_id: uuid.UUID, body: bytes) -> dict[str, Any]:
    return _transition_notification(session, key, delivery_id, body, retry=False)


def retry_notification(session: Session, key: str, delivery_id: uuid.UUID, body: bytes) -> dict[str, Any]:
    return _transition_notification(session, key, delivery_id, body, retry=True)
