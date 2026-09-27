"""Build the reviewed MAX Fleet v1 internal HTTP contract.

Run: py contracts/build_openapi.py
Requires PyYAML. The generated YAML is committed so consumers do not need Python.
"""

from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parent
REF = "#/components/schemas/"


def ref(name):
    return {"$ref": REF + name}


def obj(properties, required=(), description=None):
    value = {"type": "object", "additionalProperties": False, "properties": properties}
    if required:
        value["required"] = list(required)
    if description:
        value["description"] = description
    return value


def string(max_length=None, enum=None, pattern=None):
    value = {"type": "string"}
    if max_length:
        value["maxLength"] = max_length
    if enum:
        value["enum"] = enum
    if pattern:
        value["pattern"] = pattern
    return value


def integer(minimum=0, maximum=None):
    value = {"type": "integer", "minimum": minimum}
    if maximum is not None:
        value["maximum"] = maximum
    return value


def array(items, maximum=None):
    value = {"type": "array", "items": items}
    if maximum is not None:
        value["maxItems"] = maximum
    return value


def nullable(schema):
    return {"oneOf": [schema, {"type": "null"}]}


schemas = {
    "UUID": {"type": "string", "format": "uuid"},
    "Timestamp": {"type": "string", "format": "date-time", "description": "RFC3339 UTC"},
    "MaxID": string(pattern="^[1-9][0-9]{0,18}$"),
    "Version": integer(1),
    "FuelLevel": {"type": "integer", "enum": [0, 25, 50, 75, 100]},
    "PhotoSlot": integer(1, 8),
    "ErrorCode": string(enum=[
        "INVALID_REQUEST", "UNSUPPORTED_EVENT", "INVALID_SERVICE_TOKEN", "ACCESS_DENIED",
        "ADMIN_REQUIRED", "CANNOT_START_TRIP", "NOT_FOUND", "VEHICLE_UNAVAILABLE",
        "USER_BUSY", "STALE_VERSION", "HOLD_EXPIRED", "INVALID_STATE",
        "IDEMPOTENCY_CONFLICT", "COMMAND_IN_PROGRESS", "FILE_TOO_LARGE",
        "UNSUPPORTED_MEDIA", "PHOTO_SET_INCOMPLETE", "DUPLICATE_PHOTO",
        "ODOMETER_ROLLBACK", "LOCATION_REQUIRED", "UNSAFE_RETURN",
        "CHALLENGE_INVALID", "CHALLENGE_EXPIRED", "RULES_REQUIRED",
        "RATE_LIMITED", "DATABASE_UNAVAILABLE", "STORAGE_UNAVAILABLE",
        "TEMPORARY_FAILURE", "LEASE_EXPIRED", "CONTRACT_VERSION_UNSUPPORTED",
    ]),
    "Error": obj({
        "code": ref("ErrorCode"), "message": string(500),
        "retryable": {"type": "boolean"},
        "details": obj({"current_version": nullable(ref("Version")),
                        "missing_slots": array(ref("PhotoSlot"), 8)}, ()),
    }, ("code", "message", "retryable")),
    "ErrorResponse": obj({"error": ref("Error"), "request_id": ref("UUID")},
                         ("error", "request_id")),
    "ParkingLocation": obj({
        "id": ref("UUID"), "latitude": {"type": "number", "minimum": -90, "maximum": 90},
        "longitude": {"type": "number", "minimum": -180, "maximum": 180},
        "source": string(enum=["max_geo", "manual_map", "admin", "seed"]),
        "landmark": nullable(string(500)), "confirmed_at": ref("Timestamp"),
    }, ("id", "latitude", "longitude", "source", "landmark", "confirmed_at")),
    "Employee": obj({
        "id": ref("UUID"), "max_user_id": ref("MaxID"), "display_name": string(200),
        "role": string(enum=["employee", "admin"]), "can_start_trip": {"type": "boolean"},
        "active_trip_id": nullable(ref("UUID")), "version": ref("Version"),
        "updated_at": ref("Timestamp"),
    }, ("id", "max_user_id", "display_name", "role", "can_start_trip",
        "active_trip_id", "version", "updated_at")),
    "Me": obj({
        "allowed": {"type": "boolean"}, "max_user_id": ref("MaxID"),
        "employee": nullable(ref("Employee")),
    }, ("allowed", "max_user_id", "employee")),
    "Vehicle": obj({
        "id": ref("UUID"), "plate": string(20), "make": string(100),
        "model": string(100), "description": string(1000),
        "key_instructions": string(1000),
        "status": string(enum=["available", "holding", "in_trip", "unavailable"]),
        "manual_blocked": {"type": "boolean"}, "needs_review": {"type": "boolean"},
        "current_parking": nullable(ref("ParkingLocation")),
        "current_fuel": nullable(ref("FuelLevel")),
        "current_odometer_km": nullable(integer()),
        "fuel_confirmed_at": nullable(ref("Timestamp")),
        "odometer_confirmed_at": nullable(ref("Timestamp")),
        "known_nonblocking_issues": array(ref("UUID")),
        "version": ref("Version"), "updated_at": ref("Timestamp"),
    }, ("id", "plate", "make", "model", "description", "key_instructions",
        "status", "manual_blocked", "needs_review", "current_parking",
        "current_fuel", "current_odometer_km", "fuel_confirmed_at",
        "odometer_confirmed_at", "known_nonblocking_issues", "version", "updated_at")),
    "Inspection": obj({
        "id": ref("UUID"), "phase": string(enum=["before", "after"]),
        "status": string(enum=["draft", "finalized", "abandoned"]),
        "fuel_level": nullable(ref("FuelLevel")),
        "odometer_km": nullable(integer()), "new_damage": nullable({"type": "boolean"}),
        "cabin_clean": nullable({"type": "boolean"}),
        "parking_allowed": nullable({"type": "boolean"}),
        "keys_returned": nullable({"type": "boolean"}),
        "car_locked": nullable({"type": "boolean"}),
        "occupied_slots": array(ref("PhotoSlot"), 8),
        "missing_slots": array(ref("PhotoSlot"), 8),
        "photos_confirmed_at": nullable(ref("Timestamp")),
        "version": ref("Version"), "updated_at": ref("Timestamp"),
    }, ("id", "phase", "status", "fuel_level", "odometer_km", "new_damage",
        "cabin_clean", "parking_allowed", "keys_returned", "car_locked",
        "occupied_slots", "missing_slots", "photos_confirmed_at", "version", "updated_at")),
    "Checkout": obj({
        "id": ref("UUID"), "vehicle_id": ref("UUID"), "employee_id": ref("UUID"),
        "status": string(enum=["holding", "started", "cancelled", "expired", "rejected"]),
        "step": string(80), "expires_at": ref("Timestamp"),
        "intent_confirmed_at": nullable(ref("Timestamp")),
        "rules_version_id": nullable(ref("UUID")),
        "rules_accepted_at": nullable(ref("Timestamp")),
        "no_new_issues": nullable({"type": "boolean"}),
        "inspection": ref("Inspection"), "version": ref("Version"),
        "updated_at": ref("Timestamp"),
    }, ("id", "vehicle_id", "employee_id", "status", "step", "expires_at",
        "intent_confirmed_at", "rules_version_id", "rules_accepted_at",
        "no_new_issues", "inspection", "version", "updated_at")),
    "Return": obj({
        "id": ref("UUID"), "trip_id": ref("UUID"),
        "status": string(enum=["draft", "cancelled", "completed", "admin_closed"]),
        "step": string(80), "intent_confirmed_at": nullable(ref("Timestamp")),
        "parking_location": nullable(ref("ParkingLocation")),
        "inspection": ref("Inspection"), "version": ref("Version"),
        "updated_at": ref("Timestamp"),
    }, ("id", "trip_id", "status", "step", "intent_confirmed_at",
        "parking_location", "inspection", "version", "updated_at")),
    "Trip": obj({
        "id": ref("UUID"), "vehicle_id": ref("UUID"), "employee_id": ref("UUID"),
        "checkout_id": ref("UUID"),
        "status": string(enum=["active", "returning", "completed", "closed_by_admin"]),
        "started_at": ref("Timestamp"), "ended_at": nullable(ref("Timestamp")),
        "return_id": nullable(ref("UUID")), "missing_data": array(string(80)),
        "version": ref("Version"), "updated_at": ref("Timestamp"),
    }, ("id", "vehicle_id", "employee_id", "checkout_id", "status",
        "started_at", "ended_at", "return_id", "missing_data", "version", "updated_at")),
    "Issue": obj({
        "id": ref("UUID"), "vehicle_id": ref("UUID"), "author_id": ref("UUID"),
        "category": string(enum=["body_damage", "mechanical", "dirty", "keys", "other"]),
        "description": string(1000),
        "status": string(enum=["new", "in_progress", "resolved", "known_nonblocking"]),
        "trip_id": nullable(ref("UUID")), "inspection_id": nullable(ref("UUID")),
        "asset_ids": array(ref("UUID"), 3), "version": ref("Version"),
        "updated_at": ref("Timestamp"),
    }, ("id", "vehicle_id", "author_id", "category", "description", "status",
        "trip_id", "inspection_id", "asset_ids", "version", "updated_at")),
    "Challenge": obj({
        "id": ref("UUID"), "purpose": string(enum=["checkout", "return", "admin"]),
        "question": string(200), "options": array(string(100), 4),
        "expires_at": ref("Timestamp"), "attempts_remaining": integer(0, 3),
    }, ("id", "purpose", "question", "options", "expires_at", "attempts_remaining")),
    "Rules": obj({
        "id": ref("UUID"), "version_label": string(50), "body": string(10000),
    }, ("id", "version_label", "body")),
    "CurrentState": obj({
        "checkout": nullable(ref("Checkout")), "trip": nullable(ref("Trip")),
        "return": nullable(ref("Return")), "next_step": nullable(string(80)),
        "conversation_version": ref("Version"),
    }, ("checkout", "trip", "return", "next_step", "conversation_version")),
    "Meta": obj({
        "contract_version": {"const": "1.0"}, "build_sha": string(64),
        "mode": string(enum=["mock", "real"]), "capabilities": array(string(80)),
    }, ("contract_version", "build_sha", "mode", "capabilities")),
    "AdminSummary": obj({
        "available": integer(), "holding": integer(), "active_trips": integer(),
        "returning": integer(), "needs_review": integer(),
    }, ("available", "holding", "active_trips", "returning", "needs_review")),
    "PhotoUploadResult": obj({
        "asset_id": ref("UUID"), "sha256": string(pattern="^[a-f0-9]{64}$"),
        "inspection": ref("Inspection"),
    }, ("asset_id", "sha256", "inspection")),
    "StagedAsset": obj({
        "asset_id": ref("UUID"), "expires_at": ref("Timestamp"),
    }, ("asset_id", "expires_at")),
}


for name in ("Vehicle", "Trip", "Issue", "Employee"):
    schemas[name + "Page"] = obj({
        "items": array(ref(name)), "next_cursor": nullable(string(2048)),
    }, ("items", "next_cursor"))


def envelope(name, data_schema):
    schemas[name + "Response"] = obj({
        "data": data_schema, "request_id": ref("UUID"),
    }, ("data", "request_id"))
    return ref(name + "Response")


for name in ("Meta", "Me", "CurrentState", "Checkout", "Return", "Rules",
             "Vehicle", "Inspection", "Trip", "Issue", "Employee", "AdminSummary",
             "PhotoUploadResult", "StagedAsset", "Challenge"):
    envelope(name, ref(name))
for name in ("Vehicle", "Trip", "Issue", "Employee"):
    envelope(name + "Page", ref(name + "Page"))


spec = {
    "openapi": "3.1.0",
    "info": {"title": "MAX Fleet Data API", "version": "1.0",
             "description": "Внутренний контракт Go ↔ mock ↔ Python. Весь SQL и бизнес-транзакции принадлежат Python. JSON UUID и MAX ID — строки. Неизвестные поля отклоняются. Время RFC3339 UTC. GET проверяет actor и ownership при каждом запросе. Версия 1.0 заморожена после contract gate."},
    "servers": [{"url": "http://data-api:8000"}, {"url": "http://data-mock:8000"}],
    "tags": [{"name": name} for name in ("read", "commands", "photos", "inbox", "integration", "notifications", "health")],
    "components": {
        "securitySchemes": {
            "DataBearer": {"type": "http", "scheme": "bearer", "description": "DATA_API_TOKEN; только Go."},
            "WorkerBearer": {"type": "http", "scheme": "bearer", "description": "WORKER_API_TOKEN; отдельный секрет worker."},
        },
        "parameters": {
            "ContractVersion": {"name": "X-Contract-Version", "in": "header", "required": True,
                                "schema": {"const": "1.0"}},
            "RequestID": {"name": "X-Request-ID", "in": "header", "required": True,
                          "schema": ref("UUID")},
            "ActorMaxID": {"name": "X-Actor-Max-ID", "in": "header", "required": True,
                           "schema": ref("MaxID"), "description": "Только проверенный Go actor; клиентский заголовок не копировать."},
            "IdempotencyKey": {"name": "Idempotency-Key", "in": "header", "required": True,
                               "schema": {"type": "string", "minLength": 8, "maxLength": 200},
                               "description": "Стабилен при retry. Область: actor + ключ для команд, route + ключ для worker; тот же ключ с другим body → 409."},
            "PathID": {"name": "id", "in": "path", "required": True, "schema": ref("UUID")},
            "PathSlot": {"name": "slot", "in": "path", "required": True, "schema": ref("PhotoSlot")},
        },
        "schemas": schemas,
    },
    "paths": {},
}


ERROR_HTTP = {
    "400": "INVALID_REQUEST", "401": "INVALID_SERVICE_TOKEN", "403": "ACCESS_DENIED",
    "404": "NOT_FOUND", "409": "STALE_VERSION", "413": "FILE_TOO_LARGE",
    "415": "UNSUPPORTED_MEDIA", "422": "PHOTO_SET_INCOMPLETE", "429": "RATE_LIMITED",
    "503": "TEMPORARY_FAILURE",
}


def response(schema, status="200"):
    result = {status: {"description": "Подтверждённое состояние", "content": {
        "application/json": {"schema": schema}}}}
    for http, code in ERROR_HTTP.items():
        result[http] = {"description": code, "content": {
            "application/json": {"schema": ref("ErrorResponse")}}}
    return result


def parameters(actor=True, id_path=False, slot_path=False, extra=()):
    result = [{"$ref": "#/components/parameters/ContractVersion"},
              {"$ref": "#/components/parameters/RequestID"}]
    if actor:
        result.append({"$ref": "#/components/parameters/ActorMaxID"})
    if id_path:
        result.append({"$ref": "#/components/parameters/PathID"})
    if slot_path:
        result.append({"$ref": "#/components/parameters/PathSlot"})
    result.extend(extra)
    return result


def query(name, schema, required=False):
    return {"name": name, "in": "query", "required": required, "schema": schema}


def read(path, operation_id, data_name, *, actor=True, id_path=False,
         slot_path=False, extra=(), description=""):
    spec["paths"][path] = {"get": {
        "tags": ["read"], "operationId": operation_id,
        "description": description,
        "security": [{"DataBearer": []}],
        "parameters": parameters(actor, id_path, slot_path, extra),
        "responses": response(ref(data_name + "Response")),
    }}


P = "/internal/v1"
read(P + "/meta", "getMeta", "Meta", actor=False)
read(P + "/me", "getMe", "Me")
read(P + "/state", "getCurrentState", "CurrentState")
read(P + "/checkouts/{id}", "getCheckout", "Checkout", id_path=True,
     description="Только своё оформление либо разрешённый admin-контекст.")
read(P + "/returns/{id}", "getReturn", "Return", id_path=True,
     description="Только собственный возврат либо разрешённый admin-контекст.")
read(P + "/rules/current", "getCurrentRules", "Rules")
read(P + "/vehicles", "listVehicles", "VehiclePage", extra=(
    query("available", {"type": "boolean"}),
    query("limit", integer(1, 50)), query("cursor", string(2048)),
))
read(P + "/vehicles/{id}", "getVehicle", "Vehicle", id_path=True)
read(P + "/vehicles/{id}/previous-inspection", "getPreviousInspection", "Inspection",
     id_path=True, description="Обезличенная проекция только последнего finalized after-осмотра; не раскрывать автора, чужую поездку и произвольные asset ID.")
read(P + "/trips", "listMyTrips", "TripPage", extra=(
    query("scope", {"const": "mine"}, True), query("limit", integer(1, 50)),
    query("cursor", string(2048)),
))
read(P + "/trips/{id}", "getTrip", "Trip", id_path=True)
read(P + "/inspections/{id}", "getInspection", "Inspection", id_path=True)
read(P + "/admin/summary", "getAdminSummary", "AdminSummary")
read(P + "/admin/trips", "listAdminTrips", "TripPage", extra=(
    query("state", string(enum=["active", "returning", "completed", "closed_by_admin"])),
    query("employee_id", ref("UUID")), query("vehicle_id", ref("UUID")),
    query("limit", integer(1, 50)), query("cursor", string(2048)),
))
read(P + "/admin/employees", "listAdminEmployees", "EmployeePage", extra=(
    query("limit", integer(1, 50)), query("cursor", string(2048)),
))
read(P + "/admin/issues", "listAdminIssues", "IssuePage", extra=(
    query("status", string(enum=["new", "in_progress", "resolved", "known_nonblocking"])),
    query("vehicle_id", ref("UUID")), query("limit", integer(1, 50)),
    query("cursor", string(2048)),
))
read(P + "/issues/{id}", "getIssue", "Issue", id_path=True)
read(P + "/admin/employees/{id}", "getAdminEmployee", "Employee", id_path=True)


ROOT.joinpath("data-api.openapi.yaml").write_text(
    yaml.safe_dump(spec, allow_unicode=True, sort_keys=False, width=110), encoding="utf-8"
)
