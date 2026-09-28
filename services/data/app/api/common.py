"""Общие зависимости HTTP-слоя: service auth, заголовки контракта, ответы."""

from __future__ import annotations

import hmac
import re
import uuid
from dataclasses import dataclass
from typing import Any

from fastapi import Request
from fastapi.responses import JSONResponse

from app.config import CONTRACT_VERSION
from app.errors import DomainError

MAX_ID_RE = re.compile(r"^[1-9][0-9]{0,18}$")
UUID_RE = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
MAX_BIGINT = 9_223_372_036_854_775_807


@dataclass(frozen=True)
class CallContext:
    request_id: str
    actor_max_id: int | None


def parse_uuid(value: str) -> uuid.UUID:
    if not UUID_RE.match(value or ""):
        raise DomainError("INVALID_REQUEST")
    return uuid.UUID(value)


def parse_max_id(value: str | None) -> int:
    if value is None or not MAX_ID_RE.match(value):
        raise DomainError("INVALID_REQUEST")
    parsed = int(value)
    if parsed > MAX_BIGINT:
        raise DomainError("INVALID_REQUEST")
    return parsed


def request_id_of(request: Request) -> str:
    rid = request.headers.get("X-Request-ID", "")
    if UUID_RE.match(rid):
        return rid.lower()
    return str(uuid.uuid4())


def _check_bearer(request: Request, expected: str) -> None:
    header = request.headers.get("Authorization", "")
    if not header.startswith("Bearer "):
        raise DomainError("INVALID_SERVICE_TOKEN")
    given = header[len("Bearer "):]
    if not hmac.compare_digest(given.encode(), expected.encode()):
        raise DomainError("INVALID_SERVICE_TOKEN")


def _base_checks(request: Request, token: str) -> str:
    rid = request.headers.get("X-Request-ID", "")
    if not UUID_RE.match(rid):
        raise DomainError("INVALID_REQUEST")
    _check_bearer(request, token)
    version = request.headers.get("X-Contract-Version")
    if version != CONTRACT_VERSION:
        raise DomainError("INVALID_REQUEST" if version is None else "CONTRACT_VERSION_UNSUPPORTED")
    return rid.lower()


def service_context(request: Request, *, actor: bool = True) -> CallContext:
    settings = request.app.state.settings
    rid = _base_checks(request, settings.data_api_token)
    max_id = parse_max_id(request.headers.get("X-Actor-Max-ID")) if actor else None
    return CallContext(request_id=rid, actor_max_id=max_id)


def worker_context(request: Request) -> CallContext:
    settings = request.app.state.settings
    rid = _base_checks(request, settings.worker_api_token)
    return CallContext(request_id=rid, actor_max_id=None)


def ok(request_id: str, data: Any) -> JSONResponse:
    return JSONResponse({"data": data, "request_id": request_id})


def idempotency_key(request: Request) -> str:
    key = request.headers.get("Idempotency-Key", "")
    if not (8 <= len(key) <= 200) or any(c in key for c in "\r\n") or not key.isprintable():
        raise DomainError("INVALID_REQUEST")
    return key
