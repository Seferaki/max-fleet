"""Строгие схемы технических маршрутов worker (OpenAPI v1)."""

from __future__ import annotations

import uuid
from datetime import datetime
from typing import Annotated, Literal

from pydantic import AfterValidator, Field

from app.api.schemas import MaxIDStr, Strict
from app.errors import DomainError


def _printable(value: str) -> str:
    if any(ord(c) < 32 or ord(c) == 127 for c in value):
        raise ValueError("control characters")
    return value


def Printable(min_len: int, max_len: int) -> object:  # noqa: N802 — фабрика аннотации
    return Annotated[str, Field(min_length=min_len, max_length=max_len), AfterValidator(_printable)]


WorkerID = Annotated[str, Field(min_length=1, max_length=100), AfterValidator(_printable)]
Token = Annotated[str, Field(min_length=1, max_length=200), AfterValidator(_printable)]
Code80 = Annotated[str, Field(min_length=1, max_length=80), AfterValidator(_printable)]
Marker = Annotated[str, Field(max_length=500), AfterValidator(_printable)]


class EventPayload(Strict):
    kind: Literal["start", "text", "photo", "geo", "callback"]
    text: Annotated[str, Field(max_length=1000)] | None
    callback_data: Annotated[str, Field(max_length=200)] | None
    photo_source_key: Annotated[str, Field(max_length=500)] | None
    latitude: Annotated[float, Field(ge=-90, le=90)] | None
    longitude: Annotated[float, Field(ge=-180, le=180)] | None
    attachment_count: Annotated[int, Field(ge=0, le=100)]


class NormalizedEvent(Strict):
    integration_key: Annotated[str, Field(min_length=1, max_length=100), AfterValidator(_printable)]
    event_key: Annotated[str, Field(min_length=1, max_length=200), AfterValidator(_printable)]
    event_type: Literal["bot_started", "message_created", "message_callback"]
    actor_max_user_id: MaxIDStr
    chat_id: MaxIDStr
    message_id: Annotated[str, Field(min_length=1, max_length=200), AfterValidator(_printable)] | None
    callback_id: Annotated[str, Field(min_length=1, max_length=200), AfterValidator(_printable)] | None
    occurred_at: datetime
    payload: EventPayload

    def check_semantics(self) -> None:
        p = self.payload
        ok = False
        if self.event_type == "bot_started":
            ok = p.kind == "start" and self.message_id is None and self.callback_id is None
        elif self.event_type == "message_created":
            if self.message_id is not None and self.callback_id is None \
                    and self.event_key == f"message:{self.message_id}:message_created":
                if p.kind == "text":
                    ok = p.text is not None and p.attachment_count == 0
                elif p.kind == "photo":
                    ok = p.photo_source_key is not None and p.attachment_count == 1
                elif p.kind == "geo":
                    ok = p.latitude is not None and p.longitude is not None
        elif self.event_type == "message_callback":
            ok = (self.callback_id is not None and self.message_id is None
                  and self.event_key == f"callback:{self.callback_id}:message_callback"
                  and p.kind == "callback" and p.callback_data is not None)
        if not ok:
            raise DomainError("INVALID_REQUEST")


class QueueClaimIn(Strict):
    worker_id: WorkerID
    max_items: Annotated[int, Field(ge=1, le=50)]


class QueueAckIn(Strict):
    lease_token: Token


class QueueRetryIn(Strict):
    lease_token: Token
    error_code: Code80
    next_attempt_at: datetime


class IntegrationLeaseIn(Strict):
    worker_id: WorkerID
    expected_version: Annotated[int, Field(ge=1)]


class IntegrationCheckpointIn(Strict):
    lease_token: Token
    expected_version: Annotated[int, Field(ge=1)]
    previous_marker: Marker | None
    new_marker: Marker
    stored_event_ids: Annotated[list[uuid.UUID], Field(max_length=50)]


class NotificationAckIn(Strict):
    lease_token: Token
    provider_message_id: Annotated[str, Field(min_length=1, max_length=200), AfterValidator(_printable)]


class NotificationRetryIn(Strict):
    lease_token: Token
    error_code: Code80
    retry_after: datetime | None
    dead: bool
