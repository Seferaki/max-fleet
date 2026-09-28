"""Фотографии осмотров и замечаний.

Протокол (DATA_ENGINEER §6): проверить actor/черновик/лимиты → staged-запись →
запись объекта в S3 → короткая транзакция привязки с повторной проверкой.
Сбой S3 — фото не засчитано; сбой БД после S3 — staged-объект уберёт cleanup.
"""

from __future__ import annotations

import hashlib
import io
import uuid
import warnings
from dataclasses import dataclass
from datetime import timedelta
from typing import Any

from PIL import Image, UnidentifiedImageError
from sqlalchemy import select
from sqlalchemy.orm import Session, sessionmaker

from app import models as m
from app.config import Settings
from app.domain import dto
from app.domain.commands import InspectionContext, ensure_hold_alive, ensure_inspection_editable, \
    load_inspection_context
from app.domain.core import Actor, audit, check_version, db_now, touch
from app.domain.executor import claim_idempotency, complete_idempotency, guarded, load_actor, sweep
from app.errors import DomainError
from app.storage.object_store import ObjectStore, StorageUnavailable

FORMATS = {"image/jpeg": "JPEG", "image/png": "PNG", "image/webp": "WEBP"}
STAGED_TTL = timedelta(hours=24)


@dataclass(frozen=True)
class ValidImage:
    data: bytes
    mime: str
    sha256: str
    width: int
    height: int


def validate_image(data: bytes, content_type: str, settings: Settings) -> ValidImage:
    if not data:
        raise DomainError("INVALID_REQUEST")
    if len(data) > settings.max_upload_bytes:
        raise DomainError("FILE_TOO_LARGE")
    expected = FORMATS.get(content_type)
    if expected is None:
        raise DomainError("UNSUPPORTED_MEDIA")
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            with Image.open(io.BytesIO(data)) as probe:
                if probe.format != expected:
                    raise DomainError("UNSUPPORTED_MEDIA")
                width, height = probe.size
                if width <= 0 or height <= 0 or width * height > settings.max_pixels:
                    raise DomainError("UNSUPPORTED_MEDIA")
            with Image.open(io.BytesIO(data)) as full:
                full.load()
    except (UnidentifiedImageError, OSError, SyntaxError, ValueError, Image.DecompressionBombError,
            Image.DecompressionBombWarning) as exc:
        raise DomainError("UNSUPPORTED_MEDIA") from exc
    return ValidImage(data=data, mime=content_type, sha256=hashlib.sha256(data).hexdigest(),
                      width=width, height=height)


def _stage_asset(factory: sessionmaker[Session], actor_id: uuid.UUID, image: ValidImage, bucket: str,
                 purpose: str, scope_type: str, scope_id: uuid.UUID, event_key: str,
                 staging_ttl: timedelta) -> tuple[uuid.UUID, str]:
    asset_id = uuid.uuid4()
    object_key = f"photos/{uuid.uuid4().hex}"

    def work(session: Session) -> None:
        now = db_now(session)
        session.add(m.PhotoAsset(
            id=asset_id, object_key=object_key, bucket=bucket, sha256=image.sha256, mime_type=image.mime,
            size_bytes=len(image.data), width=image.width, height=image.height, uploaded_by=actor_id,
            source_event_key=event_key, state="staged", purpose=purpose, scope_type=scope_type,
            scope_id=scope_id, staging_expires_at=now + staging_ttl, version=1, created_at=now,
            updated_at=now))

    guarded(factory, work)
    return asset_id, object_key


def _put_object(store: ObjectStore, key: str, image: ValidImage) -> None:
    try:
        store.put(key, image.data, image.mime)
    except StorageUnavailable as exc:
        raise DomainError("STORAGE_UNAVAILABLE") from exc


# ------------------------------------------------------------------ фото осмотра

def _check_inspection_upload(session: Session, actor: Actor, inspection_id: uuid.UUID, slot: int,
                             version: int, event_key: str, sha: str) -> InspectionContext:
    ic = load_inspection_context(session, actor, inspection_id)
    now = db_now(session)
    ensure_hold_alive(session, ic, now)
    ensure_inspection_editable(ic)
    check_version(ic.inspection, version)
    existing = session.execute(select(m.InspectionPhoto.content_sha256, m.InspectionPhoto.source_event_key)
                               .where(m.InspectionPhoto.inspection_id == inspection_id)).all()
    for other_sha, other_event in existing:
        if other_sha == sha or other_event == event_key:
            raise DomainError("DUPLICATE_PHOTO")
    return ic


def upload_inspection_photo(factory: sessionmaker[Session], settings: Settings, store: ObjectStore, *,
                            max_id: int, request_id: str, key: str, inspection_id: uuid.UUID, slot: int,
                            version: int, event_key: str, image: ValidImage) -> dict[str, Any]:
    scope = f"photo:{max_id}"
    signature = hashlib.sha256(f"{inspection_id}:{slot}:{version}:{event_key}:{image.sha256}".encode()).hexdigest()
    sweep(factory)

    def precheck(session: Session) -> dict[str, Any] | None:
        actor = load_actor(session, max_id, request_id)
        record = session.scalar(select(m.IdempotencyRecord).where(
            m.IdempotencyRecord.scope == scope, m.IdempotencyRecord.key == key))
        if record is not None:
            if record.request_sha256 != signature:
                raise DomainError("IDEMPOTENCY_CONFLICT")
            if record.status == "completed" and record.response_json is not None:
                return record.response_json
        _check_inspection_upload(session, actor, inspection_id, slot, version, event_key, image.sha256)
        return None

    saved = guarded(factory, precheck)
    if saved is not None:
        return saved
    actor_id = guarded(factory, lambda s: load_actor(s, max_id, request_id).id)
    asset_id, object_key = _stage_asset(factory, actor_id, image, settings.s3_bucket, "inspection",
                                        "inspection", inspection_id, event_key, STAGED_TTL)
    _put_object(store, object_key, image)

    def attach(session: Session) -> dict[str, Any]:
        actor = load_actor(session, max_id, request_id)
        saved_result = claim_idempotency(session, scope, key, signature, actor, "photo.upload")
        if saved_result is not None:
            return saved_result
        ic = _check_inspection_upload(session, actor, inspection_id, slot, version, event_key, image.sha256)
        now = db_now(session)
        asset = session.get(m.PhotoAsset, asset_id, with_for_update=True)
        assert asset is not None
        asset.state = "ready"
        asset.stored_at = now
        asset.staging_expires_at = None
        touch(asset, now)
        old = session.get(m.InspectionPhoto, (inspection_id, slot))
        if old is not None:
            audit(session, actor, "inspection.replace_photo", "inspection", inspection_id,
                  before={"slot": slot, "asset_id": str(old.asset_id)},
                  after={"slot": slot, "asset_id": str(asset_id)})
            session.delete(old)
            session.flush()
        session.add(m.InspectionPhoto(inspection_id=inspection_id, slot=slot, asset_id=asset_id,
                                      content_sha256=image.sha256, attached_by=actor.id, attached_at=now,
                                      source_event_key=event_key))
        insp = ic.inspection
        insp.photos_confirmed_at = None
        insp.attested_at = None
        touch(insp, now)
        ic.bump_parent(now)
        session.flush()
        response = {"asset_id": str(asset_id), "sha256": image.sha256,
                    "inspection": dto.inspection_dto(session, insp)}
        complete_idempotency(session, scope, key, response, "photo_asset", str(asset_id))
        return response

    return guarded(factory, attach)


# ------------------------------------------------------------------ фото замечаний

def _stage_scope_owned(session: Session, actor: Actor, scope_type: str, scope_id: uuid.UUID) -> bool:
    if scope_type == "inspection":
        insp = session.get(m.Inspection, scope_id)
        if insp is None or insp.status != "draft":
            return False
        if insp.checkout_attempt_id is not None:
            attempt = session.get(m.CheckoutAttempt, insp.checkout_attempt_id)
            return attempt is not None and attempt.employee_id == actor.id and attempt.status == "holding"
        ret = session.get(m.ReturnAttempt, insp.return_attempt_id)
        trip = session.get(m.Trip, ret.trip_id) if ret else None
        return (ret is not None and trip is not None and trip.employee_id == actor.id
                and trip.status == "returning" and ret.status == "draft" and ret.intent_confirmed_at is not None)
    if scope_type == "trip":
        trip = session.get(m.Trip, scope_id)
        return trip is not None and trip.employee_id == actor.id and trip.status in ("active", "returning")
    assignment = session.scalar(select(m.VehicleAssignment).where(m.VehicleAssignment.vehicle_id == scope_id))
    return assignment is not None and assignment.employee_id == actor.id


def stage_issue_asset(factory: sessionmaker[Session], settings: Settings, store: ObjectStore, *,
                      max_id: int, request_id: str, key: str, scope_type: str, scope_id: uuid.UUID,
                      event_key: str, image: ValidImage) -> dict[str, Any]:
    scope = f"stage:{max_id}"
    signature = hashlib.sha256(
        f"{scope_type}:{scope_id}:{event_key}:{image.mime}:{image.sha256}".encode()).hexdigest()
    sweep(factory)

    def check(session: Session) -> dict[str, Any] | None:
        actor = load_actor(session, max_id, request_id)
        record = session.scalar(select(m.IdempotencyRecord).where(
            m.IdempotencyRecord.scope == scope, m.IdempotencyRecord.key == key))
        if record is not None:
            if record.request_sha256 != signature:
                raise DomainError("IDEMPOTENCY_CONFLICT")
            if record.status == "completed" and record.response_json is not None:
                return record.response_json
        if not _stage_scope_owned(session, actor, scope_type, scope_id):
            raise DomainError("NOT_FOUND")
        dup = session.scalar(select(m.PhotoAsset.id).where(
            m.PhotoAsset.uploaded_by == actor.id, m.PhotoAsset.purpose == "issue",
            m.PhotoAsset.scope_type == scope_type, m.PhotoAsset.scope_id == scope_id,
            m.PhotoAsset.state.in_(("staged", "ready")),
            (m.PhotoAsset.sha256 == image.sha256) | (m.PhotoAsset.source_event_key == event_key)).limit(1))
        if dup is not None:
            raise DomainError("DUPLICATE_PHOTO")
        return None

    saved = guarded(factory, check)
    if saved is not None:
        return saved
    actor_id = guarded(factory, lambda s: load_actor(s, max_id, request_id).id)
    ttl = timedelta(minutes=settings.stage_asset_minutes)
    asset_id, object_key = _stage_asset(factory, actor_id, image, settings.s3_bucket, "issue", scope_type,
                                        scope_id, event_key, ttl)
    _put_object(store, object_key, image)

    def finish(session: Session) -> dict[str, Any]:
        actor = load_actor(session, max_id, request_id)
        saved_result = claim_idempotency(session, scope, key, signature, actor, "asset.stage")
        if saved_result is not None:
            return saved_result
        if not _stage_scope_owned(session, actor, scope_type, scope_id):
            raise DomainError("NOT_FOUND")
        now = db_now(session)
        asset = session.get(m.PhotoAsset, asset_id, with_for_update=True)
        assert asset is not None
        asset.stored_at = now
        touch(asset, now)
        response = {"asset_id": str(asset_id), "expires_at": dto.ts(asset.staging_expires_at)}
        complete_idempotency(session, scope, key, response, "photo_asset", str(asset_id))
        return response

    return guarded(factory, finish)


# ------------------------------------------------------------------ чтение файлов

def _inspection_owner(session: Session, inspection_id: uuid.UUID) -> uuid.UUID | None:
    insp = session.get(m.Inspection, inspection_id)
    if insp is None:
        return None
    if insp.checkout_attempt_id is not None:
        attempt = session.get(m.CheckoutAttempt, insp.checkout_attempt_id)
        return attempt.employee_id if attempt else None
    ret = session.get(m.ReturnAttempt, insp.return_attempt_id)
    trip = session.get(m.Trip, ret.trip_id) if ret else None
    return trip.employee_id if trip else None


def authorized_asset(session: Session, actor: Actor, asset_id: uuid.UUID) -> m.PhotoAsset:
    asset = session.get(m.PhotoAsset, asset_id)
    if asset is None or asset.stored_at is None or asset.state not in ("staged", "ready"):
        raise DomainError("NOT_FOUND")
    if asset.purpose == "issue":
        allowed = asset.uploaded_by == actor.id or actor.is_admin
    else:
        link = session.scalar(select(m.InspectionPhoto).where(m.InspectionPhoto.asset_id == asset_id))
        owner = _inspection_owner(session, link.inspection_id) if link else None
        allowed = owner is not None and (owner == actor.id or actor.is_admin)
    if not allowed:
        raise DomainError("NOT_FOUND")
    return asset


def previous_photo_asset(session: Session, vehicle_id: uuid.UUID, slot: int) -> m.PhotoAsset:
    from app.domain.reads import latest_after_inspection

    if session.get(m.Vehicle, vehicle_id) is None:
        raise DomainError("NOT_FOUND")
    insp = latest_after_inspection(session, vehicle_id)
    if insp is None:
        raise DomainError("NOT_FOUND")
    link = session.get(m.InspectionPhoto, (insp.id, slot))
    if link is None:
        raise DomainError("NOT_FOUND")
    asset = session.get(m.PhotoAsset, link.asset_id)
    assert asset is not None
    return asset


def read_asset_bytes(store: ObjectStore, asset: m.PhotoAsset) -> bytes:
    try:
        data = store.get(asset.object_key)
    except StorageUnavailable as exc:
        raise DomainError("STORAGE_UNAVAILABLE") from exc
    if hashlib.sha256(data).hexdigest() != asset.sha256:
        raise DomainError("STORAGE_UNAVAILABLE")
    return data
