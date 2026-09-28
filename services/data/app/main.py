"""FastAPI приложение data-api: маршруты контракта v1."""

from __future__ import annotations

import logging
import uuid
from collections.abc import Callable
from typing import Any, TypeVar

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, Response
from sqlalchemy import text
from sqlalchemy.exc import DBAPIError
from sqlalchemy.orm import Session
from starlette.concurrency import run_in_threadpool
from starlette.datastructures import UploadFile
from starlette.exceptions import HTTPException as StarletteHTTPException

from app.api.common import (
    CallContext,
    idempotency_key,
    ok,
    parse_uuid,
    request_id_of,
    service_context,
    worker_context,
)
from app.api.schemas import OPERATIONS
from app.config import CONTRACT_VERSION, Settings
from app.db import database_ok, make_engine, make_session_factory
from app.domain import photos, queues, reads
from app.domain.core import Actor
from app.domain.executor import (
    execute_command,
    get_command_result,
    guarded,
    load_actor,
    sweep,
    translate_db_error,
)
from app.errors import DomainError
from app.storage.object_store import ObjectStore, S3ObjectStore

log = logging.getLogger("data-api")
T = TypeVar("T")
JSON_LIMIT = 64 * 1024
MULTIPART_OVERHEAD = 64 * 1024


def create_app(settings: Settings, store: ObjectStore | None = None) -> FastAPI:
    app = FastAPI(title="MAX Fleet Data API", version=CONTRACT_VERSION, docs_url=None, redoc_url=None,
                  openapi_url=None)
    engine = make_engine(settings)
    app.state.settings = settings
    app.state.engine = engine
    app.state.factory = make_session_factory(engine)
    app.state.store = store or S3ObjectStore(settings)

    # ---------------------------------------------------------------- ошибки

    @app.exception_handler(DomainError)
    async def domain_error(request: Request, exc: DomainError) -> JSONResponse:
        return JSONResponse(exc.body(request_id_of(request)), status_code=exc.http_status)

    @app.exception_handler(RequestValidationError)
    async def validation_error(request: Request, _exc: RequestValidationError) -> JSONResponse:
        return JSONResponse(DomainError("INVALID_REQUEST").body(request_id_of(request)), status_code=400)

    @app.exception_handler(StarletteHTTPException)
    async def http_error(request: Request, exc: StarletteHTTPException) -> JSONResponse:
        code = "NOT_FOUND" if exc.status_code in (404, 405) else "INVALID_REQUEST"
        return JSONResponse(DomainError(code).body(request_id_of(request)),
                            status_code=404 if code == "NOT_FOUND" else 400)

    @app.exception_handler(DBAPIError)
    async def db_error(request: Request, exc: DBAPIError) -> JSONResponse:
        err = translate_db_error(exc)
        return JSONResponse(err.body(request_id_of(request)), status_code=err.http_status)

    @app.exception_handler(Exception)
    async def unexpected(request: Request, exc: Exception) -> JSONResponse:
        log.error("unexpected error %s", type(exc).__name__)
        err = DomainError("TEMPORARY_FAILURE")
        return JSONResponse(err.body(request_id_of(request)), status_code=503)

    # ---------------------------------------------------------------- helpers

    factory = app.state.factory

    async def in_thread(fn: Callable[[], T]) -> T:
        return await run_in_threadpool(fn)

    def read_tx(fn: Callable[[Session], T]) -> T:
        return guarded(factory, fn)

    def with_actor(call: CallContext, fn: Callable[[Session, Actor], Any], *, expire: bool = False) -> Any:
        if expire:
            sweep(factory)
        assert call.actor_max_id is not None

        def work(session: Session) -> Any:
            actor = load_actor(session, call.actor_max_id, call.request_id)  # type: ignore[arg-type]
            return fn(session, actor)

        return read_tx(work)

    async def json_body(request: Request) -> bytes:
        body = await request.body()
        if len(body) > JSON_LIMIT:
            raise DomainError("INVALID_REQUEST")
        return body

    def require_json(request: Request) -> None:
        ctype = request.headers.get("content-type", "").split(";")[0].strip().lower()
        if ctype != "application/json":
            raise DomainError("INVALID_REQUEST")

    def query_of(request: Request, allowed: set[str]) -> reads.Query:
        return reads.Query(list(request.query_params.multi_items()), allowed)

    # ---------------------------------------------------------------- health / meta

    @app.get("/health/live")
    async def health_live() -> JSONResponse:
        return JSONResponse({"status": "ok"})

    @app.get("/health/ready")
    async def health_ready() -> JSONResponse:
        def check() -> bool:
            if not database_ok(engine):
                return False
            try:
                with engine.connect() as conn:
                    version = conn.execute(text("SELECT version_num FROM alembic_version")).scalar_one_or_none()
            except DBAPIError:
                return False
            from app.migrations_head import expected_head

            return version == expected_head() and app.state.store.healthy()

        ready = await in_thread(check)
        return JSONResponse({"status": "ok" if ready else "unavailable"}, status_code=200 if ready else 503)

    P = "/internal/v1"

    @app.get(P + "/meta")
    async def meta(request: Request) -> JSONResponse:
        call = service_context(request, actor=False)
        return ok(call.request_id, {"contract_version": CONTRACT_VERSION, "build_sha": settings.build_sha,
                                    "mode": "real", "capabilities": ["commands", "photos", "inbox",
                                                                     "notifications", "admin"]})

    # ---------------------------------------------------------------- чтения

    @app.get(P + "/me")
    async def me(request: Request) -> JSONResponse:
        call = service_context(request)
        max_id = call.actor_max_id
        assert max_id is not None
        return ok(call.request_id, await in_thread(lambda: read_tx(lambda s: reads.get_me(s, max_id))))

    def simple_read(path: str, fn: Callable[[Session, Actor], Any], *, expire: bool = False) -> None:
        async def handler(request: Request) -> JSONResponse:
            call = service_context(request)
            return ok(call.request_id, await in_thread(lambda: with_actor(call, fn, expire=expire)))

        app.add_api_route(path, handler, methods=["GET"])

    simple_read(P + "/rules/current", lambda s, a: reads.get_rules(s))
    simple_read(P + "/state", reads.get_state, expire=True)
    simple_read(P + "/admin/summary", reads.admin_summary, expire=True)

    def id_read(path: str, fn: Callable[[Session, Actor, uuid.UUID], Any], *, expire: bool = False) -> None:
        async def handler(request: Request, id: str) -> JSONResponse:  # noqa: A002
            call = service_context(request)
            ident = parse_uuid(id)
            return ok(call.request_id,
                      await in_thread(lambda: with_actor(call, lambda s, a: fn(s, a, ident), expire=expire)))

        app.add_api_route(path, handler, methods=["GET"])

    id_read(P + "/checkouts/{id}", reads.get_checkout, expire=True)
    id_read(P + "/returns/{id}", reads.get_return)
    id_read(P + "/vehicles/{id}", lambda s, a, i: reads.get_vehicle(s, i), expire=True)
    id_read(P + "/vehicles/{id}/previous-inspection", lambda s, a, i: reads.get_previous_inspection(s, i))
    id_read(P + "/trips/{id}", reads.get_trip)
    id_read(P + "/inspections/{id}", reads.get_inspection, expire=True)
    id_read(P + "/issues/{id}", reads.get_issue)
    id_read(P + "/admin/employees/{id}", reads.get_admin_employee)

    def list_read(path: str, allowed: set[str], fn: Callable[[Session, Actor, reads.Query], Any],
                  *, expire: bool = False) -> None:
        async def handler(request: Request) -> JSONResponse:
            call = service_context(request)
            query = query_of(request, allowed)
            return ok(call.request_id,
                      await in_thread(lambda: with_actor(call, lambda s, a: fn(s, a, query), expire=expire)))

        app.add_api_route(path, handler, methods=["GET"])

    list_read(P + "/vehicles", {"available", "limit", "cursor"},
              lambda s, a, q: reads.list_vehicles(s, q), expire=True)
    list_read(P + "/trips", {"scope", "limit", "cursor"}, reads.list_my_trips)
    list_read(P + "/admin/trips", {"state", "employee_id", "vehicle_id", "limit", "cursor"},
              reads.list_admin_trips)
    list_read(P + "/admin/employees", {"limit", "cursor"}, reads.list_admin_employees)
    list_read(P + "/admin/issues", {"status", "vehicle_id", "limit", "cursor"}, reads.list_admin_issues)

    # ---------------------------------------------------------------- команды

    @app.post(P + "/commands")
    async def commands(request: Request) -> JSONResponse:
        call = service_context(request)
        key = idempotency_key(request)
        event_id = request.headers.get("X-Inbox-Event-ID")
        lease = request.headers.get("X-Inbox-Lease")
        if (event_id is None) != (lease is None) or (lease is not None and not 1 <= len(lease) <= 200):
            raise DomainError("INVALID_REQUEST")
        if event_id is not None:
            parse_uuid(event_id)
        body = await json_body(request)
        max_id = call.actor_max_id
        assert max_id is not None
        response = await in_thread(lambda: execute_command(
            factory, settings, max_id=max_id, request_id=call.request_id, key=key, body=body,
            inbox_event_id=event_id, inbox_lease=lease))
        return ok(call.request_id, response)

    @app.get(P + "/commands/{idempotency_key}")
    async def command_result(request: Request, idempotency_key: str) -> JSONResponse:
        call = service_context(request)
        query = query_of(request, {"operation"})
        operation = query.get("operation")
        if not (8 <= len(idempotency_key) <= 200) or operation not in OPERATIONS:
            raise DomainError("INVALID_REQUEST")
        max_id = call.actor_max_id
        assert max_id is not None
        return ok(call.request_id, await in_thread(lambda: read_tx(
            lambda s: get_command_result(s, max_id, idempotency_key, operation))))

    # ---------------------------------------------------------------- фото

    async def read_form(request: Request, fields: set[str]) -> tuple[dict[str, str], bytes, str]:
        length = request.headers.get("content-length")
        if length is not None and length.isdigit() and int(length) > settings.max_upload_bytes + MULTIPART_OVERHEAD:
            raise DomainError("FILE_TOO_LARGE")
        ctype = request.headers.get("content-type", "")
        if not ctype.lower().startswith("multipart/form-data"):
            raise DomainError("INVALID_REQUEST")
        try:
            form = await request.form(max_files=1, max_fields=len(fields) + 1, max_part_size=1024)
        except Exception as exc:  # noqa: BLE001 — любые ошибки разбора multipart
            raise DomainError("INVALID_REQUEST") from exc
        values: dict[str, str] = {}
        image: bytes | None = None
        image_type = ""
        try:
            for name, value in form.multi_items():
                if name not in fields or name in values or (name == "image" and image is not None):
                    raise DomainError("INVALID_REQUEST")
                if name == "image":
                    if not isinstance(value, UploadFile):
                        raise DomainError("INVALID_REQUEST")
                    image = await value.read(settings.max_upload_bytes + 1)
                    image_type = (value.content_type or "").split(";")[0].strip().lower()
                else:
                    if isinstance(value, UploadFile):
                        raise DomainError("INVALID_REQUEST")
                    values[name] = value
        finally:
            await form.close()
        if image is None or set(values) != fields - {"image"}:
            raise DomainError("INVALID_REQUEST")
        if len(image) > settings.max_upload_bytes:
            raise DomainError("FILE_TOO_LARGE")
        return values, image, image_type

    def check_event_key(value: str) -> str:
        if not 1 <= len(value) <= 200 or any(ord(c) < 32 for c in value):
            raise DomainError("INVALID_REQUEST")
        return value

    @app.post(P + "/inspections/{id}/photos/{slot}")
    async def upload_photo(request: Request, id: str, slot: str) -> JSONResponse:  # noqa: A002
        call = service_context(request)
        inspection_id = parse_uuid(id)
        if not slot.isdigit() or not 1 <= int(slot) <= 8:
            raise DomainError("INVALID_REQUEST")
        key = idempotency_key(request)
        values, data, ctype = await read_form(request, {"image", "expected_version", "source_event_key"})
        raw_version = values["expected_version"]
        if not raw_version.isdigit() or int(raw_version) < 1:
            raise DomainError("INVALID_REQUEST")
        event_key = check_event_key(values["source_event_key"])
        image = await in_thread(lambda: photos.validate_image(data, ctype, settings))
        max_id = call.actor_max_id
        assert max_id is not None
        result = await in_thread(lambda: photos.upload_inspection_photo(
            factory, settings, app.state.store, max_id=max_id, request_id=call.request_id, key=key,
            inspection_id=inspection_id, slot=int(slot), version=int(raw_version), event_key=event_key,
            image=image))
        return ok(call.request_id, result)

    @app.post(P + "/assets/stage")
    async def stage_asset(request: Request) -> JSONResponse:
        call = service_context(request)
        key = idempotency_key(request)
        values, data, ctype = await read_form(
            request, {"image", "purpose", "scope_type", "scope_id", "source_event_key"})
        if values["purpose"] != "issue" or values["scope_type"] not in ("vehicle", "trip", "inspection"):
            raise DomainError("INVALID_REQUEST")
        scope_id = parse_uuid(values["scope_id"])
        event_key = check_event_key(values["source_event_key"])
        image = await in_thread(lambda: photos.validate_image(data, ctype, settings))
        max_id = call.actor_max_id
        assert max_id is not None
        result = await in_thread(lambda: photos.stage_issue_asset(
            factory, settings, app.state.store, max_id=max_id, request_id=call.request_id, key=key,
            scope_type=values["scope_type"], scope_id=scope_id, event_key=event_key, image=image))
        return ok(call.request_id, result)

    def binary(call: CallContext, data: bytes, mime: str) -> Response:
        return Response(data, media_type=mime, headers={"Cache-Control": "private, no-store",
                                                        "X-Request-ID": call.request_id})

    @app.get(P + "/assets/{id}/content")
    async def asset_content(request: Request, id: str) -> Response:  # noqa: A002
        call = service_context(request)
        ident = parse_uuid(id)
        asset = await in_thread(lambda: with_actor(call, lambda s, a: photos.authorized_asset(s, a, ident)))
        data = await in_thread(lambda: photos.read_asset_bytes(app.state.store, asset))
        return binary(call, data, asset.mime_type)

    @app.get(P + "/vehicles/{id}/previous-inspection/photos/{slot}")
    async def previous_photo(request: Request, id: str, slot: str) -> Response:  # noqa: A002
        call = service_context(request)
        ident = parse_uuid(id)
        if not slot.isdigit() or not 1 <= int(slot) <= 8:
            raise DomainError("INVALID_REQUEST")
        asset = await in_thread(lambda: with_actor(
            call, lambda s, a: photos.previous_photo_asset(s, ident, int(slot))))
        data = await in_thread(lambda: photos.read_asset_bytes(app.state.store, asset))
        return binary(call, data, asset.mime_type)

    # ---------------------------------------------------------------- worker

    def worker_guard(request: Request) -> CallContext:
        call = worker_context(request)
        if request.headers.get("X-Actor-Max-ID") is not None:
            raise DomainError("INVALID_REQUEST")
        return call

    def worker_post(path: str, fn: Callable[..., Any], *, path_kind: str | None = None) -> None:
        async def handler(request: Request) -> JSONResponse:
            call = worker_guard(request)
            key = idempotency_key(request)
            require_json(request)
            body = await json_body(request)
            args: list[Any] = [key]
            if path_kind == "id":
                args.append(parse_uuid(request.path_params["id"]))
            elif path_kind == "key":
                integration_key = request.path_params["key"]
                if not 1 <= len(integration_key) <= 100:
                    raise DomainError("INVALID_REQUEST")
                args.append(integration_key)
            args.append(body)
            return ok(call.request_id, await in_thread(lambda: read_tx(lambda s: fn(s, *args))))

        app.add_api_route(path, handler, methods=["POST"])

    worker_post(P + "/inbox", queues.store_inbox)
    worker_post(P + "/inbox/claim", queues.claim_inbox)
    worker_post(P + "/inbox/{id}/ack", queues.ack_inbox, path_kind="id")
    worker_post(P + "/inbox/{id}/retry", queues.retry_inbox, path_kind="id")
    worker_post(P + "/integrations/{key}/lease", queues.lease_integration, path_kind="key")
    worker_post(P + "/integrations/{key}/checkpoint", queues.checkpoint_integration, path_kind="key")
    worker_post(P + "/notifications/claim", queues.claim_notifications)
    worker_post(P + "/notifications/{id}/ack", queues.ack_notification, path_kind="id")
    worker_post(P + "/notifications/{id}/retry", queues.retry_notification, path_kind="id")

    @app.get(P + "/integrations/{key}")
    async def integration(request: Request, key: str) -> JSONResponse:
        call = worker_guard(request)
        if not 1 <= len(key) <= 100:
            raise DomainError("INVALID_REQUEST")
        return ok(call.request_id, await in_thread(lambda: read_tx(lambda s: queues.get_integration(s, key))))

    return app


def app_from_env() -> FastAPI:
    logging.basicConfig(level=logging.INFO)
    return create_app(Settings.from_env())
