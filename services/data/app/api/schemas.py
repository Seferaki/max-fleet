"""Строгие схемы payload команд (OpenAPI v1, additionalProperties=false)."""

from __future__ import annotations

import json
import math
import uuid
from datetime import datetime
from typing import Annotated, Any, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator

from app.errors import DomainError

MaxIDStr = Annotated[str, Field(pattern=r"^[1-9][0-9]{0,18}$")]
Text1000 = Annotated[str, Field(max_length=1000)]
Fuel = Literal[0, 25, 50, 75, 100]
IssueCategory = Literal["body_damage", "mechanical", "cleanliness", "keys", "parking", "car_lock", "other"]
NonNegInt = Annotated[int, Field(ge=0, le=10_000_000)]


class Strict(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)


class EmptyPayload(Strict):
    pass


class ChallengeIntent(Strict):
    operation: Literal["checkout.create", "trip.begin_return", "vehicle.block", "vehicle.unblock",
                       "employee.grant", "employee.access", "trip.admin_close"]
    target_id: uuid.UUID | None
    expected_version: Annotated[int, Field(ge=1)] | None
    reason: Text1000 | None = None
    max_user_id: MaxIDStr | None = None
    display_name: Annotated[str, Field(max_length=200)] | None = None
    can_start_trip: bool | None = None
    review_completed: bool | None = None


class ChallengeCreate(Strict):
    purpose: Literal["take", "return", "vehicle_block", "vehicle_unblock", "employee_grant",
                     "employee_access", "admin_close"]
    intent_payload: ChallengeIntent


class ChallengeAnswer(Strict):
    selected_option: Annotated[int, Field(ge=0, le=3)]


class AcceptRules(Strict):
    rules_version_id: uuid.UUID


class InspectionUpdate(Strict):
    fuel_level: Fuel | None = None
    odometer_km: NonNegInt | None = None
    new_damage: bool | None = None
    cabin_clean: bool | None = None
    parking_allowed: bool | None = None
    keys_returned: bool | None = None
    car_locked: bool | None = None


class NoNewIssues(Strict):
    value: Literal[True]


class Attestation(Strict):
    attestation: Literal[True]


class LocationInput(Strict):
    latitude: Annotated[float, Field(ge=-90, le=90)]
    longitude: Annotated[float, Field(ge=-180, le=180)]
    source: Literal["max_geo", "manual_map", "admin"]
    landmark: Annotated[str, Field(max_length=500)] | None = None
    confirmed: Literal[True]

    @field_validator("latitude", "longitude")
    @classmethod
    def finite(cls, value: float) -> float:
        if not math.isfinite(value):
            raise ValueError("not finite")
        return value


class IssueCreate(Strict):
    category: IssueCategory
    description: Annotated[str, Field(min_length=1, max_length=1000)]
    trip_id: uuid.UUID | None = None
    inspection_id: uuid.UUID | None = None
    asset_ids: Annotated[list[uuid.UUID], Field(max_length=3)]


class VehicleBlock(Strict):
    reason: Annotated[str, Field(min_length=1, max_length=1000)]
    challenge_id: uuid.UUID


class VehicleUnblock(Strict):
    reason: Annotated[str, Field(min_length=1, max_length=1000)]
    review_completed: Literal[True]
    challenge_id: uuid.UUID


class VehicleEdit(Strict):
    description: Text1000 | None = None
    key_instructions: Text1000 | None = None
    confirmation: Literal[True]


class VehicleCorrectSnapshot(Strict):
    reason: Annotated[str, Field(min_length=1, max_length=1000)]
    fuel_level: Fuel | None = None
    odometer_km: NonNegInt | None = None
    location: LocationInput | None = None
    confirmation: Literal[True]


class VehicleAnnotate(Strict):
    reason: Annotated[str, Field(min_length=1, max_length=1000)]
    text: Annotated[str, Field(min_length=1, max_length=1000)]
    confirmation: Literal[True]


class EmployeeGrant(Strict):
    max_user_id: MaxIDStr
    display_name: Annotated[str, Field(min_length=1, max_length=200)]
    challenge_id: uuid.UUID


class EmployeeAccess(Strict):
    can_start_trip: bool
    reason: Annotated[str, Field(min_length=1, max_length=1000)]
    challenge_id: uuid.UUID


class IssueResolve(Strict):
    status: Literal["in_progress", "resolved", "known_nonblocking"]
    comment: Annotated[str, Field(min_length=1, max_length=1000)]
    confirmation: Literal[True]


class AdminCloseData(Strict):
    fuel_level: Fuel | None = None
    odometer_km: NonNegInt | None = None
    latitude: Annotated[float, Field(ge=-90, le=90)] | None = None
    longitude: Annotated[float, Field(ge=-180, le=180)] | None = None
    landmark: Annotated[str, Field(max_length=500)] | None = None
    keys_returned: bool | None = None
    car_locked: bool | None = None


class TripAdminClose(Strict):
    reason: Annotated[str, Field(min_length=1, max_length=1000)]
    challenge_id: uuid.UUID
    available_data: AdminCloseData | None = None


ConversationFlow = Literal[
    "issue_before", "issue_during", "issue_after", "return_location", "issue_post_return",
    "issue_admin_resolution", "vehicle_odometer_correction", "trip_admin_close",
]


class ConversationContext(Strict):
    target_id: uuid.UUID | None = None
    selected_slot: Annotated[int, Field(ge=1, le=8)] | None = None
    challenge_id: uuid.UUID | None = None
    vehicle_id: uuid.UUID | None = None
    trip_id: uuid.UUID | None = None
    return_id: uuid.UUID | None = None
    issue_id: uuid.UUID | None = None
    issue_version: Annotated[int, Field(ge=1)] | None = None
    trip_version: Annotated[int, Field(ge=1)] | None = None
    cursor: Annotated[str, Field(max_length=2048)] | None = None
    draft_text: Annotated[str, Field(max_length=1000)] | None = None
    issue_category: IssueCategory | None = None
    asset_ids: list[uuid.UUID] = Field(default_factory=list, max_length=3)
    vehicle_version: Annotated[int, Field(ge=1)] | None = None
    correction_odometer_km: NonNegInt | None = None
    admin_close_data: AdminCloseData | None = None
    challenge_version: Annotated[int, Field(ge=1)] | None = None
    challenge_question: Annotated[str, Field(max_length=200)] | None = None
    challenge_options: list[Annotated[int, Field(ge=0, le=18)]] = Field(default_factory=list, max_length=4)
    challenge_expires_at: datetime | None = None

    @field_validator("asset_ids")
    @classmethod
    def asset_ids_are_unique(cls, value: list[uuid.UUID]) -> list[uuid.UUID]:
        if len(value) != len(set(value)):
            raise ValueError("asset_ids must be unique")
        return value


class ConversationSave(Strict):
    flow: ConversationFlow
    step: Annotated[str, Field(min_length=1, max_length=80)]
    context: ConversationContext
    pending_input_kind: Literal["text", "photo", "geo", "none"] | None = None


# target: "existing" — UUID обязателен; "new" — null; "optional" — UUID либо null.
OPERATIONS: dict[str, tuple[str, type[Strict]]] = {
    "checkout.create": ("existing", EmptyPayload),
    "checkout.cancel": ("existing", EmptyPayload),
    "challenge.create": ("optional", ChallengeCreate),
    "challenge.answer": ("existing", ChallengeAnswer),
    "checkout.accept_rules": ("existing", AcceptRules),
    "inspection.update": ("existing", InspectionUpdate),
    "inspection.confirm_photos": ("existing", EmptyPayload),
    "checkout.set_no_new_issues": ("existing", NoNewIssues),
    "checkout.start": ("existing", Attestation),
    "trip.begin_return": ("existing", EmptyPayload),
    "return.cancel": ("existing", EmptyPayload),
    "return.set_location": ("existing", LocationInput),
    "return.complete": ("existing", Attestation),
    "issue.create": ("existing", IssueCreate),
    "vehicle.block": ("existing", VehicleBlock),
    "vehicle.unblock": ("existing", VehicleUnblock),
    "vehicle.edit": ("existing", VehicleEdit),
    "vehicle.correct_snapshot": ("existing", VehicleCorrectSnapshot),
    "vehicle.annotate": ("existing", VehicleAnnotate),
    "employee.grant": ("new", EmployeeGrant),
    "employee.access": ("existing", EmployeeAccess),
    "issue.resolve": ("existing", IssueResolve),
    "trip.admin_close": ("existing", TripAdminClose),
    "conversation.save": ("existing", ConversationSave),
}


class Command:
    def __init__(self, operation: str, target_id: uuid.UUID | None, expected_version: int | None,
                 payload: Any, raw_payload: dict[str, Any]) -> None:
        self.operation = operation
        self.target_id = target_id
        self.expected_version = expected_version
        self.payload = payload
        self.raw_payload = raw_payload


def _no_nulls(value: Any) -> bool:
    """В payload команд null запрещён там, где поле необязательно: пропуск ≠ null."""
    if isinstance(value, dict):
        return all(v is not None and _no_nulls(v) for v in value.values())
    if isinstance(value, list):
        return all(_no_nulls(v) for v in value)
    return True


NULLABLE_PAYLOAD_FIELDS = {
    ("issue.create", "trip_id"), ("issue.create", "inspection_id"),
    ("conversation.save", "pending_input_kind"),
    ("challenge.create", "intent_payload"),  # внутри допускаются null target_id/expected_version
    ("conversation.save", "context"),
}


def parse_command(body: bytes) -> Command:
    try:
        data = json.loads(body)
    except (ValueError, UnicodeDecodeError) as exc:
        raise DomainError("INVALID_REQUEST") from exc
    if not isinstance(data, dict) or set(data) != {"operation", "target_id", "expected_version", "payload"}:
        raise DomainError("INVALID_REQUEST")
    operation = data["operation"]
    if not isinstance(operation, str) or operation not in OPERATIONS:
        raise DomainError("INVALID_REQUEST")
    kind, model = OPERATIONS[operation]
    target_raw, version_raw, payload = data["target_id"], data["expected_version"], data["payload"]
    if not isinstance(payload, dict):
        raise DomainError("INVALID_REQUEST")
    target: uuid.UUID | None = None
    version: int | None = None
    if kind == "new":
        if target_raw is not None or version_raw is not None:
            raise DomainError("INVALID_REQUEST")
    elif kind == "existing" or target_raw is not None or version_raw is not None:
        if not isinstance(target_raw, str) or isinstance(version_raw, bool) or not isinstance(version_raw, int) \
                or version_raw < 1:
            raise DomainError("INVALID_REQUEST")
        try:
            target = uuid.UUID(target_raw)
        except ValueError as exc:
            raise DomainError("INVALID_REQUEST") from exc
        if len(target_raw) != 36:
            raise DomainError("INVALID_REQUEST")
        version = version_raw
    for key, value in payload.items():
        if value is None and (operation, key) not in NULLABLE_PAYLOAD_FIELDS:
            raise DomainError("INVALID_REQUEST")
    try:
        parsed = model.model_validate_json(json.dumps(payload), strict=True)
    except ValidationError as exc:
        raise DomainError("INVALID_REQUEST") from exc
    return Command(operation, target, version, parsed, payload)


def canonical_sha(value: Any) -> str:
    import hashlib
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"),
                                     ensure_ascii=False).encode()).hexdigest()
