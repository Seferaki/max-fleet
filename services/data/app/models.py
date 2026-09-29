"""Модель данных MAX Fleet — 21 таблица из docs/DATABASE.md.

Схема создаётся только миграциями Alembic. Все FK — ON DELETE RESTRICT.
"""

from __future__ import annotations

import uuid
from datetime import datetime
from decimal import Decimal
from typing import Any

from sqlalchemy import (
    BigInteger,
    Boolean,
    CheckConstraint,
    DateTime,
    ForeignKey,
    ForeignKeyConstraint,
    Identity,
    Index,
    Integer,
    MetaData,
    Numeric,
    SmallInteger,
    String,
    Text,
    UniqueConstraint,
    func,
    text,
)
from sqlalchemy.dialects.postgresql import JSONB, UUID
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column

NAMING = {
    "ix": "ix_%(table_name)s_%(column_0_N_name)s",
    "uq": "uq_%(table_name)s_%(column_0_N_name)s",
    "ck": "ck_%(table_name)s_%(constraint_name)s",
    "fk": "fk_%(table_name)s_%(column_0_name)s_%(referred_table_name)s",
    "pk": "pk_%(table_name)s",
}


class Base(DeclarativeBase):
    metadata = MetaData(naming_convention=NAMING)


def uuid_pk() -> Mapped[uuid.UUID]:
    return mapped_column(UUID(as_uuid=True), primary_key=True, default=uuid.uuid4)


def fk(target: str, *, nullable: bool = False, **kwargs: Any) -> Mapped[Any]:
    return mapped_column(UUID(as_uuid=True), ForeignKey(target, ondelete="RESTRICT", **kwargs),
                         nullable=nullable)


def ts(*, nullable: bool = False) -> Mapped[Any]:
    return mapped_column(DateTime(timezone=True), nullable=nullable)


def created() -> Mapped[datetime]:
    return mapped_column(DateTime(timezone=True), nullable=False, server_default=func.now())


def version_col() -> Mapped[int]:
    return mapped_column(BigInteger, nullable=False, server_default=text("1"), default=1)


FUEL_CHECK = "IN (0, 25, 50, 75, 100)"


# ---------------------------------------------------------------- справочники

class Employee(Base):
    __tablename__ = "employees"
    id: Mapped[uuid.UUID] = uuid_pk()
    max_user_id: Mapped[int] = mapped_column(BigInteger, nullable=False, unique=True)
    display_name: Mapped[str] = mapped_column(Text, nullable=False)
    role: Mapped[str] = mapped_column(Text, nullable=False)
    can_start_trip: Mapped[bool] = mapped_column(Boolean, nullable=False, server_default=text("true"))
    access_block_reason: Mapped[str | None] = mapped_column(Text)
    access_changed_by: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    max_chat_id: Mapped[int | None] = mapped_column(BigInteger)
    bot_started_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("role IN ('employee', 'admin')", name="role"),
        CheckConstraint("char_length(display_name) BETWEEN 1 AND 200", name="display_name_len"),
        CheckConstraint("max_user_id > 0", name="max_user_id_positive"),
        CheckConstraint("can_start_trip OR access_block_reason IS NOT NULL", name="block_reason"),
        CheckConstraint("version > 0", name="version_positive"),
        Index("ix_employees_role_can_start_trip", "role", "can_start_trip"),
    )


class RulesVersion(Base):
    __tablename__ = "rules_versions"
    id: Mapped[uuid.UUID] = uuid_pk()
    version_label: Mapped[str] = mapped_column(Text, nullable=False, unique=True)
    body: Mapped[str] = mapped_column(Text, nullable=False)
    body_sha256: Mapped[str] = mapped_column(String(64), nullable=False)
    effective_at: Mapped[datetime] = ts()
    is_current: Mapped[bool] = mapped_column(Boolean, nullable=False, server_default=text("false"))
    created_at: Mapped[datetime] = created()
    __table_args__ = (
        CheckConstraint("char_length(body) BETWEEN 1 AND 10000", name="body_len"),
        CheckConstraint("char_length(version_label) BETWEEN 1 AND 50", name="label_len"),
        Index("uq_rules_versions_current", "is_current", unique=True,
              postgresql_where=text("is_current")),
    )


class Vehicle(Base):
    __tablename__ = "vehicles"
    id: Mapped[uuid.UUID] = uuid_pk()
    plate: Mapped[str] = mapped_column(Text, nullable=False)
    plate_normalized: Mapped[str] = mapped_column(Text, nullable=False, unique=True)
    make: Mapped[str] = mapped_column(Text, nullable=False)
    model: Mapped[str] = mapped_column(Text, nullable=False)
    description: Mapped[str] = mapped_column(Text, nullable=False, server_default=text("''"))
    key_instructions: Mapped[str] = mapped_column(Text, nullable=False, server_default=text("''"))
    manual_blocked: Mapped[bool] = mapped_column(Boolean, nullable=False, server_default=text("false"))
    block_reason: Mapped[str | None] = mapped_column(Text)
    blocked_by: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    needs_review: Mapped[bool] = mapped_column(Boolean, nullable=False, server_default=text("false"))
    current_parking_location_id: Mapped[uuid.UUID | None] = fk(
        "parking_locations.id", nullable=True, use_alter=True)
    current_fuel: Mapped[int | None] = mapped_column(SmallInteger)
    fuel_confirmed_at: Mapped[datetime | None] = ts(nullable=True)
    current_odometer_km: Mapped[int | None] = mapped_column(BigInteger)
    odometer_confirmed_at: Mapped[datetime | None] = ts(nullable=True)
    last_inspection_id: Mapped[uuid.UUID | None] = fk("inspections.id", nullable=True, use_alter=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint(f"current_fuel IS NULL OR current_fuel {FUEL_CHECK}", name="fuel"),
        CheckConstraint("current_odometer_km IS NULL OR current_odometer_km >= 0", name="odometer"),
        CheckConstraint("NOT manual_blocked OR char_length(block_reason) > 0", name="block_reason"),
        CheckConstraint("char_length(plate) BETWEEN 1 AND 20", name="plate_len"),
        CheckConstraint("char_length(description) <= 1000 AND char_length(key_instructions) <= 1000",
                        name="text_len"),
        CheckConstraint("version > 0", name="version_positive"),
    )


# ---------------------------------------------------------------- оформление и поездка

class CheckoutAttempt(Base):
    __tablename__ = "checkout_attempts"
    id: Mapped[uuid.UUID] = uuid_pk()
    employee_id: Mapped[uuid.UUID] = fk("employees.id")
    vehicle_id: Mapped[uuid.UUID] = fk("vehicles.id")
    status: Mapped[str] = mapped_column(Text, nullable=False)
    step: Mapped[str] = mapped_column(Text, nullable=False)
    expires_at: Mapped[datetime] = ts()
    intent_confirmed_at: Mapped[datetime | None] = ts(nullable=True)
    rules_version_id: Mapped[uuid.UUID | None] = fk("rules_versions.id", nullable=True)
    rules_accepted_at: Mapped[datetime | None] = ts(nullable=True)
    no_new_issues: Mapped[bool | None] = mapped_column(Boolean)
    ended_at: Mapped[datetime | None] = ts(nullable=True)
    end_reason: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("status IN ('holding', 'started', 'cancelled', 'expired', 'rejected')",
                        name="status"),
        CheckConstraint("(rules_version_id IS NULL) = (rules_accepted_at IS NULL)", name="rules_pair"),
        CheckConstraint("version > 0", name="version_positive"),
        Index("ix_checkout_attempts_employee_created", "employee_id", "created_at", "id"),
        Index("ix_checkout_attempts_vehicle_created", "vehicle_id", "created_at", "id"),
        Index("ix_checkout_attempts_holding_expires", "expires_at",
              postgresql_where=text("status = 'holding'")),
    )


class Trip(Base):
    __tablename__ = "trips"
    id: Mapped[uuid.UUID] = uuid_pk()
    checkout_attempt_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("checkout_attempts.id", ondelete="RESTRICT"),
        nullable=False, unique=True)
    employee_id: Mapped[uuid.UUID] = fk("employees.id")
    vehicle_id: Mapped[uuid.UUID] = fk("vehicles.id")
    status: Mapped[str] = mapped_column(Text, nullable=False)
    started_at: Mapped[datetime] = ts()
    ended_at: Mapped[datetime | None] = ts(nullable=True)
    closed_by: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    close_reason: Mapped[str | None] = mapped_column(Text)
    missing_data: Mapped[list[str]] = mapped_column(JSONB, nullable=False,
                                                     server_default=text("'[]'::jsonb"), default=list)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("status IN ('active', 'returning', 'completed', 'closed_by_admin')", name="status"),
        CheckConstraint(
            "(status IN ('active', 'returning') AND ended_at IS NULL) OR "
            "(status IN ('completed', 'closed_by_admin') AND ended_at IS NOT NULL AND ended_at >= started_at)",
            name="ended"),
        CheckConstraint(
            "status <> 'closed_by_admin' OR (closed_by IS NOT NULL AND char_length(close_reason) > 0)",
            name="admin_close"),
        CheckConstraint("jsonb_typeof(missing_data) = 'array'", name="missing_data_array"),
        CheckConstraint("version > 0", name="version_positive"),
        Index("uq_trips_open_vehicle", "vehicle_id", unique=True,
              postgresql_where=text("status IN ('active', 'returning')")),
        Index("uq_trips_open_employee", "employee_id", unique=True,
              postgresql_where=text("status IN ('active', 'returning')")),
        Index("ix_trips_employee_started", "employee_id", text("started_at DESC"), "id"),
        Index("ix_trips_vehicle_started", "vehicle_id", text("started_at DESC"), "id"),
        Index("ix_trips_status_started", "status", text("started_at DESC"), "id"),
    )


class VehicleAssignment(Base):
    """Единая защита от двойной выдачи: одна строка на машину и на сотрудника."""

    __tablename__ = "vehicle_assignments"
    vehicle_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("vehicles.id", ondelete="RESTRICT"), primary_key=True)
    employee_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("employees.id", ondelete="RESTRICT"), nullable=False, unique=True)
    checkout_attempt_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("checkout_attempts.id", ondelete="RESTRICT"),
        nullable=False, unique=True)
    trip_id: Mapped[uuid.UUID | None] = mapped_column(
        UUID(as_uuid=True), ForeignKey("trips.id", ondelete="RESTRICT"), unique=True)
    phase: Mapped[str] = mapped_column(Text, nullable=False)
    hold_expires_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint(
            "(phase = 'hold' AND trip_id IS NULL AND hold_expires_at IS NOT NULL) OR "
            "(phase = 'trip' AND trip_id IS NOT NULL AND hold_expires_at IS NULL)",
            name="phase"),
        CheckConstraint("version > 0", name="version_positive"),
    )


class ReturnAttempt(Base):
    __tablename__ = "return_attempts"
    id: Mapped[uuid.UUID] = uuid_pk()
    trip_id: Mapped[uuid.UUID] = fk("trips.id")
    status: Mapped[str] = mapped_column(Text, nullable=False)
    step: Mapped[str] = mapped_column(Text, nullable=False)
    intent_confirmed_at: Mapped[datetime | None] = ts(nullable=True)
    parking_location_id: Mapped[uuid.UUID | None] = fk("parking_locations.id", nullable=True,
                                                         use_alter=True)
    cancelled_at: Mapped[datetime | None] = ts(nullable=True)
    completed_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("status IN ('draft', 'cancelled', 'completed', 'admin_closed')", name="status"),
        CheckConstraint("version > 0", name="version_positive"),
        Index("uq_return_attempts_draft", "trip_id", unique=True,
              postgresql_where=text("status = 'draft'")),
        Index("ix_return_attempts_trip", "trip_id", "created_at"),
    )


class Inspection(Base):
    __tablename__ = "inspections"
    id: Mapped[uuid.UUID] = uuid_pk()
    phase: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(Text, nullable=False)
    checkout_attempt_id: Mapped[uuid.UUID | None] = mapped_column(
        UUID(as_uuid=True), ForeignKey("checkout_attempts.id", ondelete="RESTRICT"), unique=True)
    return_attempt_id: Mapped[uuid.UUID | None] = mapped_column(
        UUID(as_uuid=True), ForeignKey("return_attempts.id", ondelete="RESTRICT"), unique=True)
    author_id: Mapped[uuid.UUID] = fk("employees.id")
    fuel_level: Mapped[int | None] = mapped_column(SmallInteger)
    odometer_km: Mapped[int | None] = mapped_column(BigInteger)
    new_damage: Mapped[bool | None] = mapped_column(Boolean)
    cabin_clean: Mapped[bool | None] = mapped_column(Boolean)
    parking_allowed: Mapped[bool | None] = mapped_column(Boolean)
    keys_returned: Mapped[bool | None] = mapped_column(Boolean)
    car_locked: Mapped[bool | None] = mapped_column(Boolean)
    photos_confirmed_at: Mapped[datetime | None] = ts(nullable=True)
    attested_at: Mapped[datetime | None] = ts(nullable=True)
    finalized_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("phase IN ('before', 'after')", name="phase"),
        CheckConstraint("status IN ('draft', 'finalized', 'abandoned')", name="status"),
        CheckConstraint(
            "(phase = 'before' AND checkout_attempt_id IS NOT NULL AND return_attempt_id IS NULL) OR "
            "(phase = 'after' AND return_attempt_id IS NOT NULL AND checkout_attempt_id IS NULL)",
            name="owner"),
        CheckConstraint(f"fuel_level IS NULL OR fuel_level {FUEL_CHECK}", name="fuel"),
        CheckConstraint("odometer_km IS NULL OR odometer_km >= 0", name="odometer"),
        CheckConstraint(
            "status <> 'finalized' OR (fuel_level IS NOT NULL AND odometer_km IS NOT NULL "
            "AND photos_confirmed_at IS NOT NULL AND finalized_at IS NOT NULL)",
            name="finalized_complete"),
        CheckConstraint(
            "status <> 'finalized' OR phase <> 'after' OR (new_damage IS NOT NULL AND cabin_clean IS NOT NULL "
            "AND parking_allowed IS NOT NULL AND keys_returned IS NOT NULL AND car_locked IS NOT NULL)",
            name="after_answers"),
        CheckConstraint("version > 0", name="version_positive"),
    )


class ParkingLocation(Base):
    __tablename__ = "parking_locations"
    id: Mapped[uuid.UUID] = uuid_pk()
    vehicle_id: Mapped[uuid.UUID] = fk("vehicles.id")
    return_attempt_id: Mapped[uuid.UUID | None] = fk("return_attempts.id", nullable=True)
    latitude: Mapped[Decimal] = mapped_column(Numeric(9, 6), nullable=False)
    longitude: Mapped[Decimal] = mapped_column(Numeric(9, 6), nullable=False)
    source: Mapped[str] = mapped_column(Text, nullable=False)
    landmark: Mapped[str | None] = mapped_column(Text)
    address: Mapped[str | None] = mapped_column(Text)
    author_id: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    confirmed_at: Mapped[datetime] = ts()
    created_at: Mapped[datetime] = created()
    __table_args__ = (
        CheckConstraint("latitude BETWEEN -90 AND 90", name="latitude"),
        CheckConstraint("longitude BETWEEN -180 AND 180", name="longitude"),
        CheckConstraint("source IN ('max_geo', 'manual_map', 'admin', 'seed')", name="source"),
        CheckConstraint("landmark IS NULL OR char_length(landmark) <= 500", name="landmark_len"),
        CheckConstraint("source = 'seed' OR author_id IS NOT NULL", name="author"),
        CheckConstraint("source NOT IN ('admin', 'seed') OR return_attempt_id IS NULL", name="admin_no_return"),
        Index("ix_parking_locations_vehicle_confirmed", "vehicle_id", text("confirmed_at DESC")),
    )


# ---------------------------------------------------------------- фото и замечания

class PhotoAsset(Base):
    __tablename__ = "photo_assets"
    id: Mapped[uuid.UUID] = uuid_pk()
    object_key: Mapped[str] = mapped_column(Text, nullable=False, unique=True)
    bucket: Mapped[str] = mapped_column(Text, nullable=False)
    sha256: Mapped[str] = mapped_column(String(64), nullable=False)
    mime_type: Mapped[str] = mapped_column(Text, nullable=False)
    size_bytes: Mapped[int] = mapped_column(BigInteger, nullable=False)
    width: Mapped[int] = mapped_column(Integer, nullable=False)
    height: Mapped[int] = mapped_column(Integer, nullable=False)
    uploaded_by: Mapped[uuid.UUID] = fk("employees.id")
    source_event_key: Mapped[str | None] = mapped_column(Text)
    state: Mapped[str] = mapped_column(Text, nullable=False)
    purpose: Mapped[str] = mapped_column(Text, nullable=False)
    scope_type: Mapped[str] = mapped_column(Text, nullable=False)
    scope_id: Mapped[uuid.UUID] = mapped_column(UUID(as_uuid=True), nullable=False)
    staging_expires_at: Mapped[datetime | None] = ts(nullable=True)
    stored_at: Mapped[datetime | None] = ts(nullable=True)
    deleted_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        UniqueConstraint("id", "sha256", name="uq_photo_assets_id_sha256"),
        CheckConstraint("size_bytes > 0 AND size_bytes <= 10485760", name="size"),
        CheckConstraint("width > 0 AND height > 0 AND width::bigint * height <= 25000000", name="pixels"),
        CheckConstraint("sha256 ~ '^[a-f0-9]{64}$'", name="sha256_hex"),
        CheckConstraint("mime_type IN ('image/jpeg', 'image/png', 'image/webp')", name="mime"),
        CheckConstraint("state IN ('staged', 'ready', 'delete_pending', 'deleted')", name="state"),
        CheckConstraint("purpose IN ('inspection', 'issue')", name="purpose"),
        CheckConstraint("scope_type IN ('inspection', 'trip', 'vehicle')", name="scope_type"),
        Index("ix_photo_assets_state_staging", "state", "staging_expires_at"),
        Index("ix_photo_assets_scope", "uploaded_by", "scope_type", "scope_id"),
    )


class InspectionPhoto(Base):
    __tablename__ = "inspection_photos"
    inspection_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("inspections.id", ondelete="RESTRICT"), primary_key=True)
    slot: Mapped[int] = mapped_column(SmallInteger, primary_key=True)
    asset_id: Mapped[uuid.UUID] = mapped_column(UUID(as_uuid=True), nullable=False, unique=True)
    content_sha256: Mapped[str] = mapped_column(String(64), nullable=False)
    attached_by: Mapped[uuid.UUID] = fk("employees.id")
    attached_at: Mapped[datetime] = ts()
    source_event_key: Mapped[str] = mapped_column(Text, nullable=False)
    __table_args__ = (
        CheckConstraint("slot BETWEEN 1 AND 8", name="slot"),
        UniqueConstraint("inspection_id", "content_sha256", name="uq_inspection_photos_sha"),
        UniqueConstraint("inspection_id", "source_event_key", name="uq_inspection_photos_event"),
        ForeignKeyConstraint(["asset_id", "content_sha256"], ["photo_assets.id", "photo_assets.sha256"],
                             ondelete="RESTRICT", name="fk_inspection_photos_asset"),
    )


class Issue(Base):
    __tablename__ = "issues"
    id: Mapped[uuid.UUID] = uuid_pk()
    vehicle_id: Mapped[uuid.UUID] = fk("vehicles.id")
    author_id: Mapped[uuid.UUID] = fk("employees.id")
    trip_id: Mapped[uuid.UUID | None] = fk("trips.id", nullable=True)
    inspection_id: Mapped[uuid.UUID | None] = fk("inspections.id", nullable=True)
    stage: Mapped[str] = mapped_column(Text, nullable=False)
    category: Mapped[str] = mapped_column(Text, nullable=False)
    description: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(Text, nullable=False, server_default=text("'open'"))
    blocks_issuance: Mapped[bool] = mapped_column(Boolean, nullable=False, server_default=text("true"))
    resolution_comment: Mapped[str | None] = mapped_column(Text)
    resolved_by: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    resolved_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("stage IN ('before', 'during', 'return', 'after')", name="stage"),
        CheckConstraint("category IN ('body_damage', 'mechanical', 'cleanliness', 'keys', 'other')",
                        name="category"),
        CheckConstraint("char_length(description) BETWEEN 1 AND 1000", name="description_len"),
        CheckConstraint("status IN ('open', 'in_progress', 'resolved', 'known_nonblocking')", name="status"),
        CheckConstraint(
            "(status IN ('open', 'in_progress') AND blocks_issuance) OR "
            "(status IN ('resolved', 'known_nonblocking') AND NOT blocks_issuance AND resolved_by IS NOT NULL "
            "AND resolved_at IS NOT NULL AND char_length(resolution_comment) > 0)",
            name="resolution"),
        CheckConstraint("version > 0", name="version_positive"),
        Index("ix_issues_vehicle_status_created", "vehicle_id", "status", "created_at"),
        Index("ix_issues_status_created", "status", "created_at"),
        Index("ix_issues_trip", "trip_id"),
        Index("ix_issues_updated", text("updated_at DESC"), "id"),
    )


class IssuePhoto(Base):
    __tablename__ = "issue_photos"
    issue_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("issues.id", ondelete="RESTRICT"), primary_key=True)
    ordinal: Mapped[int] = mapped_column(SmallInteger, primary_key=True)
    asset_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("photo_assets.id", ondelete="RESTRICT"), nullable=False, unique=True)
    attached_at: Mapped[datetime] = ts()
    __table_args__ = (CheckConstraint("ordinal BETWEEN 1 AND 3", name="ordinal"),)


# ---------------------------------------------------------------- подтверждения и надёжность

class Challenge(Base):
    __tablename__ = "challenges"
    id: Mapped[uuid.UUID] = uuid_pk()
    actor_id: Mapped[uuid.UUID] = fk("employees.id")
    purpose: Mapped[str] = mapped_column(Text, nullable=False)
    target_type: Mapped[str] = mapped_column(Text, nullable=False)
    target_id: Mapped[uuid.UUID | None] = mapped_column(UUID(as_uuid=True))
    target_version: Mapped[int | None] = mapped_column(BigInteger)
    intent_payload: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    intent_sha256: Mapped[str] = mapped_column(String(64), nullable=False)
    operand_a: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    operand_b: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    options: Mapped[list[int]] = mapped_column(JSONB, nullable=False)
    correct_answer: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    wrong_attempts: Mapped[int] = mapped_column(SmallInteger, nullable=False, server_default=text("0"),
                                                default=0)
    expires_at: Mapped[datetime] = ts()
    solved_at: Mapped[datetime | None] = ts(nullable=True)
    consumed_at: Mapped[datetime | None] = ts(nullable=True)
    invalidated_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint(
            "purpose IN ('take', 'return', 'vehicle_block', 'vehicle_unblock', 'employee_grant', "
            "'employee_access', 'admin_close')", name="purpose"),
        CheckConstraint("operand_a BETWEEN 1 AND 9 AND operand_b BETWEEN 1 AND 9", name="operands"),
        CheckConstraint("jsonb_typeof(options) = 'array' AND jsonb_array_length(options) = 4", name="options"),
        CheckConstraint("wrong_attempts BETWEEN 0 AND 3", name="attempts"),
        CheckConstraint("version > 0", name="version_positive"),
        Index("ix_challenges_actor_purpose_expires", "actor_id", "purpose", "expires_at"),
    )


class ConversationState(Base):
    __tablename__ = "conversation_states"
    employee_id: Mapped[uuid.UUID] = mapped_column(
        UUID(as_uuid=True), ForeignKey("employees.id", ondelete="RESTRICT"), primary_key=True)
    chat_id: Mapped[int | None] = mapped_column(BigInteger)
    flow: Mapped[str] = mapped_column(Text, nullable=False)
    step: Mapped[str] = mapped_column(Text, nullable=False)
    context: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    pending_input_kind: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("pending_input_kind IS NULL OR pending_input_kind IN ('text', 'photo', 'geo', 'none')",
                        name="pending_kind"),
        CheckConstraint("version > 0", name="version_positive"),
    )


class IntegrationState(Base):
    __tablename__ = "integration_state"
    integration_key: Mapped[str] = mapped_column(Text, primary_key=True)
    mode: Mapped[str] = mapped_column(Text, nullable=False)
    poll_marker: Mapped[str | None] = mapped_column(Text)
    poller_lease_owner: Mapped[str | None] = mapped_column(Text)
    poller_lease_token: Mapped[str | None] = mapped_column(Text)
    poller_lease_until: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        CheckConstraint("mode IN ('webhook', 'polling')", name="mode"),
        CheckConstraint("version > 0", name="version_positive"),
    )


class InboundEvent(Base):
    __tablename__ = "inbound_events"
    id: Mapped[uuid.UUID] = uuid_pk()
    integration_key: Mapped[str] = mapped_column(
        Text, ForeignKey("integration_state.integration_key", ondelete="RESTRICT"), nullable=False)
    event_key: Mapped[str] = mapped_column(Text, nullable=False)
    event_type: Mapped[str] = mapped_column(Text, nullable=False)
    actor_id: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    actor_max_user_id: Mapped[int] = mapped_column(BigInteger, nullable=False)
    payload: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    payload_schema_version: Mapped[int] = mapped_column(Integer, nullable=False, server_default=text("1"))
    received_at: Mapped[datetime] = ts()
    sequence: Mapped[int] = mapped_column(BigInteger, Identity(always=True), nullable=False, unique=True)
    status: Mapped[str] = mapped_column(Text, nullable=False, server_default=text("'pending'"))
    attempt_count: Mapped[int] = mapped_column(Integer, nullable=False, server_default=text("0"))
    lease_owner: Mapped[str | None] = mapped_column(Text)
    lease_token: Mapped[str | None] = mapped_column(Text)
    lease_until: Mapped[datetime | None] = ts(nullable=True)
    next_attempt_at: Mapped[datetime] = ts()
    last_error_code: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        UniqueConstraint("integration_key", "event_key", name="uq_inbound_events_key"),
        CheckConstraint("status IN ('pending', 'processing', 'done', 'retry', 'dead')", name="status"),
        CheckConstraint("event_type IN ('bot_started', 'message_created', 'message_callback')",
                        name="event_type"),
        Index("ix_inbound_events_queue", "status", "next_attempt_at", "received_at"),
        Index("ix_inbound_events_actor", "actor_max_user_id", "sequence"),
    )


class IdempotencyRecord(Base):
    __tablename__ = "idempotency_records"
    id: Mapped[uuid.UUID] = uuid_pk()
    scope: Mapped[str] = mapped_column(Text, nullable=False)
    key: Mapped[str] = mapped_column(Text, nullable=False)
    request_sha256: Mapped[str] = mapped_column(String(64), nullable=False)
    actor_id: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    operation: Mapped[str | None] = mapped_column(Text)
    status: Mapped[str] = mapped_column(Text, nullable=False)
    http_status: Mapped[int | None] = mapped_column(Integer)
    response_json: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    resource_type: Mapped[str | None] = mapped_column(Text)
    resource_id: Mapped[uuid.UUID | None] = mapped_column(UUID(as_uuid=True))
    expires_at: Mapped[datetime] = ts()
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        UniqueConstraint("scope", "key", name="uq_idempotency_records_scope_key"),
        CheckConstraint("status IN ('processing', 'completed')", name="status"),
        Index("ix_idempotency_records_expires", "expires_at"),
    )


class OutboxEvent(Base):
    __tablename__ = "outbox_events"
    id: Mapped[uuid.UUID] = uuid_pk()
    event_type: Mapped[str] = mapped_column(Text, nullable=False)
    aggregate_type: Mapped[str] = mapped_column(Text, nullable=False)
    aggregate_id: Mapped[uuid.UUID] = mapped_column(UUID(as_uuid=True), nullable=False)
    aggregate_version: Mapped[int] = mapped_column(BigInteger, nullable=False)
    payload: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    schema_version: Mapped[int] = mapped_column(Integer, nullable=False, server_default=text("1"))
    occurred_at: Mapped[datetime] = ts()
    expanded_at: Mapped[datetime | None] = ts(nullable=True)
    created_at: Mapped[datetime] = created()
    __table_args__ = (
        UniqueConstraint("event_type", "aggregate_type", "aggregate_id", "aggregate_version",
                         name="uq_outbox_events_aggregate"),
        CheckConstraint(
            "event_type IN ('trip_started', 'trip_completed', 'trip_admin_closed', 'issue_created', "
            "'issue_resolved', 'vehicle_blocked', 'access_changed')", name="event_type"),
        Index("ix_outbox_events_unexpanded", "occurred_at", postgresql_where=text("expanded_at IS NULL")),
    )


class NotificationDelivery(Base):
    __tablename__ = "notification_deliveries"
    id: Mapped[uuid.UUID] = uuid_pk()
    event_id: Mapped[uuid.UUID] = fk("outbox_events.id")
    recipient_id: Mapped[uuid.UUID] = fk("employees.id")
    channel: Mapped[str] = mapped_column(Text, nullable=False, server_default=text("'max'"))
    status: Mapped[str] = mapped_column(Text, nullable=False, server_default=text("'pending'"))
    attempt_count: Mapped[int] = mapped_column(Integer, nullable=False, server_default=text("0"))
    next_attempt_at: Mapped[datetime] = ts()
    lease_owner: Mapped[str | None] = mapped_column(Text)
    lease_token: Mapped[str | None] = mapped_column(Text)
    lease_until: Mapped[datetime | None] = ts(nullable=True)
    sent_at: Mapped[datetime | None] = ts(nullable=True)
    provider_message_id: Mapped[str | None] = mapped_column(Text)
    last_error_code: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = created()
    updated_at: Mapped[datetime] = created()
    version: Mapped[int] = version_col()
    __table_args__ = (
        UniqueConstraint("event_id", "recipient_id", "channel", name="uq_notification_deliveries_target"),
        CheckConstraint("status IN ('pending', 'sending', 'sent', 'retry', 'dead')", name="status"),
        Index("ix_notification_deliveries_queue", "status", "next_attempt_at"),
    )


class AuditLog(Base):
    __tablename__ = "audit_log"
    id: Mapped[uuid.UUID] = uuid_pk()
    actor_id: Mapped[uuid.UUID | None] = fk("employees.id", nullable=True)
    actor_kind: Mapped[str] = mapped_column(Text, nullable=False)
    action: Mapped[str] = mapped_column(Text, nullable=False)
    entity_type: Mapped[str] = mapped_column(Text, nullable=False)
    entity_id: Mapped[uuid.UUID] = mapped_column(UUID(as_uuid=True), nullable=False)
    reason: Mapped[str | None] = mapped_column(Text)
    before_json: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    after_json: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    request_id: Mapped[uuid.UUID] = mapped_column(UUID(as_uuid=True), nullable=False)
    created_at: Mapped[datetime] = created()
    __table_args__ = (
        CheckConstraint("actor_kind IN ('employee', 'admin', 'system')", name="actor_kind"),
        Index("ix_audit_log_entity", "entity_type", "entity_id", "created_at", "id"),
        Index("ix_audit_log_actor", "actor_id", "created_at"),
    )


ALL_TABLES = sorted(Base.metadata.tables)
