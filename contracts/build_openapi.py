"""Build the reviewed MAX Fleet v1.6 internal HTTP contract.

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
    "IssueCategory": string(enum=["body_damage", "mechanical", "cleanliness", "keys", "parking", "car_lock", "other"]),
    "ConversationFlow": string(enum=["issue_before", "issue_during", "issue_after", "return_location", "issue_post_return"]),
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
        "before_inspection": ref("Inspection"),
        "after_inspection": nullable(ref("Inspection")),
        "parking_location": nullable(ref("ParkingLocation")),
        "issues": array(ref("Issue")),
        "version": ref("Version"), "updated_at": ref("Timestamp"),
    }, ("id", "vehicle_id", "employee_id", "checkout_id", "status",
        "started_at", "ended_at", "return_id", "missing_data", "before_inspection",
        "after_inspection", "parking_location", "issues", "version", "updated_at")),
    "Issue": obj({
        "id": ref("UUID"), "vehicle_id": ref("UUID"), "author_id": ref("UUID"),
        "stage": string(enum=["before", "during", "return", "after", "post_return"]),
        "category": ref("IssueCategory"),
        "description": string(1000),
        "status": string(enum=["open", "in_progress", "resolved", "known_nonblocking"]),
        "blocks_issuance": {"type": "boolean"},
        "trip_id": nullable(ref("UUID")), "inspection_id": nullable(ref("UUID")),
        "asset_ids": array(ref("UUID"), 3), "version": ref("Version"),
        "updated_at": ref("Timestamp"),
    }, ("id", "vehicle_id", "author_id", "stage", "category", "description", "status",
        "blocks_issuance",
        "trip_id", "inspection_id", "asset_ids", "version", "updated_at")),
    "Challenge": obj({
        "id": ref("UUID"), "purpose": ref("ChallengePurpose"),
        "question": string(200), "options": {"type": "array", "items": integer(0, 18),
                                               "minItems": 4, "maxItems": 4,
                                               "uniqueItems": True},
        "expires_at": ref("Timestamp"), "attempts_remaining": integer(0, 3),
        "version": ref("Version"), "updated_at": ref("Timestamp"),
    }, ("id", "purpose", "question", "options", "expires_at",
        "attempts_remaining", "version", "updated_at")),
    "ChallengePurpose": string(enum=["take", "return", "vehicle_block",
                                     "vehicle_unblock", "employee_grant",
                                     "employee_access", "admin_close"]),
    "Rules": obj({
        "id": ref("UUID"), "version_label": string(50), "body": string(10000),
    }, ("id", "version_label", "body")),
    "CurrentState": obj({
        "checkout": nullable(ref("Checkout")), "trip": nullable(ref("Trip")),
        "return": nullable(ref("Return")), "next_step": nullable(string(80)),
        "conversation": nullable(ref("Conversation")),
        "conversation_version": ref("Version"),
    }, ("checkout", "trip", "return", "next_step", "conversation", "conversation_version")),
    "Meta": obj({
        "contract_version": {"const": "1.6"}, "build_sha": string(64),
        "mode": string(enum=["mock", "real"]), "capabilities": array(string(80)),
    }, ("contract_version", "build_sha", "mode", "capabilities")),
    "AdminSummary": obj({
        "available": integer(), "holding": integer(), "active_trips": integer(),
        "returning": integer(), "needs_review": integer(), "open_issues": integer(),
    }, ("available", "holding", "active_trips", "returning", "needs_review",
        "open_issues")),
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
             "PhotoUploadResult", "StagedAsset"):
    envelope(name, ref(name))
for name in ("Vehicle", "Trip", "Issue", "Employee"):
    envelope(name + "Page", ref(name + "Page"))


spec = {
    "openapi": "3.1.0",
    "info": {"title": "MAX Fleet Data API", "version": "1.6",
             "description": "Внутренний контракт Go ↔ mock ↔ Python. Весь SQL и бизнес-транзакции принадлежат Python. JSON UUID и MAX ID — строки. Неизвестные поля отклоняются. Время RFC3339 UTC. GET проверяет actor и ownership при каждом запросе. Версия 1.6 связывает доступные данные admin close с SHA-256 намерения, поэтому proof подтверждает все изменения поездки и snapshot."},
    "servers": [{"url": "http://data-api:8000"}, {"url": "http://data-mock:8000"}],
    "tags": [{"name": name, "description": description} for name, description in (
        ("read", "Чтение доменных данных с проверкой actor и прав"),
        ("commands", "Идемпотентные бизнес-команды"),
        ("photos", "Приватное хранение и чтение фотографий"),
        ("inbox", "Надёжный приём и обработка MAX событий"),
        ("integration", "Lease и marker режима polling"),
        ("notifications", "Очередь доставки уведомлений"),
        ("health", "Проверки процесса и готовности"),
    )],
    "components": {
        "securitySchemes": {
            "DataBearer": {"type": "http", "scheme": "bearer", "description": "DATA_API_TOKEN; только Go."},
            "WorkerBearer": {"type": "http", "scheme": "bearer", "description": "WORKER_API_TOKEN; отдельный секрет worker."},
        },
        "parameters": {
            "ContractVersion": {"name": "X-Contract-Version", "in": "header", "required": True,
                                "schema": {"const": "1.6"}},
            "RequestID": {"name": "X-Request-ID", "in": "header", "required": True,
                          "schema": ref("UUID")},
            "ActorMaxID": {"name": "X-Actor-Max-ID", "in": "header", "required": True,
                           "schema": ref("MaxID"), "description": "Только проверенный Go actor; клиентский заголовок не копировать."},
            "IdempotencyKey": {"name": "Idempotency-Key", "in": "header", "required": True,
                               "schema": {"type": "string", "minLength": 8, "maxLength": 200},
                               "description": "Стабилен при retry. Область: actor + ключ для команд, route + ключ для worker; тот же ключ с другим body → 409."},
            "PathID": {"name": "id", "in": "path", "required": True, "schema": ref("UUID")},
            "PathSlot": {"name": "slot", "in": "path", "required": True, "schema": ref("PhotoSlot")},
            "PathPhotoPhase": {"name": "phase", "in": "path", "required": True,
                               "schema": {"type": "string", "enum": ["before", "after"]}},
        },
        "schemas": schemas,
    },
    "paths": {},
}


ERROR_HTTP = {
    "400": "INVALID_REQUEST", "401": "INVALID_SERVICE_TOKEN",
    "403": "ACCESS_DENIED for reads, ADMIN_REQUIRED for admin commands, or CANNOT_START_TRIP",
    "404": "NOT_FOUND", "409": "STALE_VERSION or INVALID_STATE", "413": "FILE_TOO_LARGE",
    "415": "UNSUPPORTED_MEDIA",
    "422": "PHOTO_SET_INCOMPLETE, CHALLENGE_EXPIRED, RULES_REQUIRED, or other business validation",
    "429": "RATE_LIMITED", "503": "TEMPORARY_FAILURE",
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
        "tags": ["read"], "operationId": operation_id, "summary": operation_id,
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
    query("status", string(enum=["open", "in_progress", "resolved", "known_nonblocking"])),
    query("vehicle_id", ref("UUID")), query("limit", integer(1, 50)),
    query("cursor", string(2048)),
))
read(P + "/issues/{id}", "getIssue", "Issue", id_path=True)
read(P + "/admin/employees/{id}", "getAdminEmployee", "Employee", id_path=True)


schemas["Conversation"] = obj({
    "flow": ref("ConversationFlow"), "step": string(80),
    "context": obj({"target_id": nullable(ref("UUID")),
                    "selected_slot": nullable(ref("PhotoSlot")),
                    "challenge_id": nullable(ref("UUID")),
                    "vehicle_id": nullable(ref("UUID")),
                    "trip_id": nullable(ref("UUID")),
                    "return_id": nullable(ref("UUID")),
                    "issue_id": nullable(ref("UUID")),
                    "cursor": nullable(string(2048)),
                    "draft_text": nullable(string(1000)),
                    "issue_category": nullable(ref("IssueCategory")),
                    "asset_ids": {**array(ref("UUID"), 3), "uniqueItems": True},
                    "vehicle_version": nullable(ref("Version"))}),
    "pending_input_kind": nullable(string(enum=["text", "photo", "geo", "none"])),
    "version": ref("Version"), "updated_at": ref("Timestamp"),
}, ("flow", "step", "context", "pending_input_kind", "version", "updated_at"),
    "issue_post_return belongs to the actor's own completed trip. target_id and trip_id are that trip ID; vehicle_id and vehicle_version bind the draft to its vehicle snapshot. draft_text, issue_category and up to three trip-scoped asset_ids survive restart. Saving the issue uses issue.create with trip_id and inspection_id=null; it must not update the completed trip or finalized after-inspection.")

schemas["LocationInput"] = obj({
    "latitude": {"type": "number", "minimum": -90, "maximum": 90},
    "longitude": {"type": "number", "minimum": -180, "maximum": 180},
    "source": string(enum=["max_geo", "manual_map", "admin"]),
    "landmark": nullable(string(500)), "confirmed": {"const": True},
}, ("latitude", "longitude", "source", "confirmed"))

schemas["CheckoutCreateIntent"] = obj({
    "operation": {"const": "checkout.create"},
    "target_id": ref("UUID"), "expected_version": ref("Version"),
}, ("operation", "target_id", "expected_version"))
schemas["TripBeginReturnIntent"] = obj({
    "operation": {"const": "trip.begin_return"},
    "target_id": ref("UUID"), "expected_version": ref("Version"),
}, ("operation", "target_id", "expected_version"))
schemas["VehicleBlockIntent"] = obj({
    "operation": {"const": "vehicle.block"},
    "target_id": ref("UUID"), "expected_version": ref("Version"),
    "reason": string(1000),
}, ("operation", "target_id", "expected_version", "reason"))
schemas["VehicleUnblockIntent"] = obj({
    "operation": {"const": "vehicle.unblock"},
    "target_id": ref("UUID"), "expected_version": ref("Version"),
    "reason": string(1000), "review_completed": {"const": True},
}, ("operation", "target_id", "expected_version", "reason", "review_completed"))
schemas["EmployeeGrantIntent"] = obj({
    "operation": {"const": "employee.grant"},
    "target_id": {"type": "null"}, "expected_version": {"type": "null"},
    "max_user_id": ref("MaxID"), "display_name": string(200),
}, ("operation", "target_id", "expected_version", "max_user_id", "display_name"))
schemas["EmployeeAccessIntent"] = obj({
    "operation": {"const": "employee.access"},
    "target_id": ref("UUID"), "expected_version": ref("Version"),
    "can_start_trip": {"type": "boolean"}, "reason": string(1000),
}, ("operation", "target_id", "expected_version", "can_start_trip", "reason"))
schemas["TripAdminCloseIntent"] = obj({
    "operation": {"const": "trip.admin_close"},
    "target_id": ref("UUID"), "expected_version": ref("Version"),
    "reason": string(1000), "available_data": ref("AdminCloseData"),
}, ("operation", "target_id", "expected_version", "reason"))
schemas["ChallengeIntent"] = {
    "oneOf": [ref(name) for name in (
        "CheckoutCreateIntent", "TripBeginReturnIntent", "VehicleBlockIntent",
        "VehicleUnblockIntent", "EmployeeGrantIntent", "EmployeeAccessIntent",
        "TripAdminCloseIntent")],
    "description": "Ровно один operation-specific набор полей; его SHA-256 считается от UTF-8 JSON с сортировкой ключей, компактными разделителями ',' и ':' и неэкранированными Unicode (эквивалент json.dumps(sort_keys=True, separators=(',', ':'), ensure_ascii=False)).",
}

challenge_create_payloads = (
    ("TakeChallengePayload", "take", "CheckoutCreateIntent"),
    ("ReturnChallengePayload", "return", "TripBeginReturnIntent"),
    ("VehicleBlockChallengePayload", "vehicle_block", "VehicleBlockIntent"),
    ("VehicleUnblockChallengePayload", "vehicle_unblock", "VehicleUnblockIntent"),
    ("EmployeeGrantChallengePayload", "employee_grant", "EmployeeGrantIntent"),
    ("EmployeeAccessChallengePayload", "employee_access", "EmployeeAccessIntent"),
    ("AdminCloseChallengePayload", "admin_close", "TripAdminCloseIntent"),
)
for schema_name, purpose, intent_name in challenge_create_payloads:
    schemas[schema_name] = obj({
        "purpose": {"const": purpose},
        "intent_payload": {"allOf": [ref("ChallengeIntent"), ref(intent_name)]},
    }, ("purpose", "intent_payload"))
schemas["ChallengeCreatePayload"] = {
    "oneOf": [ref(schema_name) for schema_name, _, _ in challenge_create_payloads],
    "description": "Purpose и operation intent обязаны соответствовать друг другу.",
}

schemas["AdminCloseData"] = obj({
    "fuel_level": ref("FuelLevel"), "odometer_km": integer(),
    "latitude": {"type": "number", "minimum": -90, "maximum": 90},
    "longitude": {"type": "number", "minimum": -180, "maximum": 180},
    "landmark": string(500), "keys_returned": {"type": "boolean"},
    "car_locked": {"type": "boolean"},
})


def payload(fields, required=(), min_properties=None):
    value = obj(fields, required)
    if min_properties is not None:
        value["minProperties"] = min_properties
    return value


command_specs = [
    ("checkout.create", "existing", payload({}), "Checkout"),
    ("checkout.cancel", "existing", payload({}), "Checkout"),
    ("challenge.create", "optional", ref("ChallengeCreatePayload"), "Challenge"),
    ("challenge.answer", "existing", payload({
        "selected_option": integer(0, 3),
    }, ("selected_option",)), "Challenge"),
    ("checkout.accept_rules", "existing", payload({
        "rules_version_id": ref("UUID"),
    }, ("rules_version_id",)), "Checkout"),
    ("inspection.update", "existing", payload({
        "fuel_level": ref("FuelLevel"), "odometer_km": integer(),
        "new_damage": {"type": "boolean"}, "cabin_clean": {"type": "boolean"},
        "parking_allowed": {"type": "boolean"}, "keys_returned": {"type": "boolean"},
        "car_locked": {"type": "boolean"},
    }, min_properties=1), "Inspection"),
    ("inspection.confirm_photos", "existing", payload({}), "Inspection"),
    ("checkout.set_no_new_issues", "existing", payload({
        "value": {"const": True},
    }, ("value",)), "Checkout"),
    ("checkout.start", "existing", payload({
        "attestation": {"const": True},
    }, ("attestation",)), "Trip"),
    ("trip.begin_return", "existing", payload({}), "Return"),
    ("return.cancel", "existing", payload({}), "Return"),
    ("return.set_location", "existing", ref("LocationInput"), "Return"),
    ("return.complete", "existing", payload({
        "attestation": {"const": True},
    }, ("attestation",)), "Return"),
    ("issue.create", "existing", payload({
        "category": ref("IssueCategory"), "description": string(1000),
        "trip_id": nullable(ref("UUID")),
        "inspection_id": nullable(ref("UUID")),
        "asset_ids": array(ref("UUID"), 3),
    }, ("category", "description", "asset_ids")), "Issue"),
    ("vehicle.block", "existing", payload({
        "reason": string(1000), "challenge_id": ref("UUID"),
    }, ("reason", "challenge_id")), "Vehicle"),
    ("vehicle.unblock", "existing", payload({
        "reason": string(1000), "review_completed": {"const": True},
        "challenge_id": ref("UUID"),
    }, ("reason", "review_completed", "challenge_id")), "Vehicle"),
    ("vehicle.edit", "existing", payload({
        "description": string(1000), "key_instructions": string(1000),
        "confirmation": {"const": True},
    }, ("confirmation",)), "Vehicle"),
    ("vehicle.correct_snapshot", "existing", payload({
        "reason": string(1000), "fuel_level": ref("FuelLevel"),
        "odometer_km": integer(), "location": ref("LocationInput"),
        "confirmation": {"const": True},
    }, ("reason", "confirmation")), "Vehicle"),
    ("vehicle.annotate", "existing", payload({
        "reason": string(1000), "text": string(1000),
        "confirmation": {"const": True},
    }, ("reason", "text", "confirmation")), "Vehicle"),
    ("employee.grant", "new", payload({
        "max_user_id": ref("MaxID"), "display_name": string(200),
        "challenge_id": ref("UUID"),
    }, ("max_user_id", "display_name", "challenge_id")), "Employee"),
    ("employee.access", "existing", payload({
        "can_start_trip": {"type": "boolean"}, "reason": string(1000),
        "challenge_id": ref("UUID"),
    }, ("can_start_trip", "reason", "challenge_id")), "Employee"),
    ("issue.resolve", "existing", payload({
        "status": string(enum=["in_progress", "resolved", "known_nonblocking"]),
        "comment": string(1000), "confirmation": {"const": True},
    }, ("status", "comment", "confirmation")), "Issue"),
    ("trip.admin_close", "existing", payload({
        "reason": string(1000), "challenge_id": ref("UUID"),
        "available_data": ref("AdminCloseData"),
    }, ("reason", "challenge_id")), "Trip"),
    ("conversation.save", "existing", payload({
        "flow": ref("ConversationFlow"), "step": string(80),
        "context": schemas["Conversation"]["properties"]["context"],
        "pending_input_kind": nullable(string(enum=["text", "photo", "geo", "none"])),
    }, ("flow", "step", "context")), "Conversation"),
]

command_refs = []
for operation, target_kind, body, result_name in command_specs:
    name = "".join(part.capitalize() for part in operation.replace(".", "_").split("_")) + "Command"
    if isinstance(body, dict) and "$ref" in body:
        payload_schema = body
    else:
        payload_schema = body
    target_schema = ref("UUID") if target_kind == "existing" else {"type": "null"}
    version_schema = ref("Version") if target_kind == "existing" else {"type": "null"}
    if target_kind == "optional":
        target_schema = nullable(ref("UUID"))
        version_schema = nullable(ref("Version"))
    schemas[name] = obj({
        "operation": {"const": operation}, "target_id": target_schema,
        "expected_version": version_schema, "payload": payload_schema,
    }, ("operation", "target_id", "expected_version", "payload"))
    command_refs.append(ref(name))

schemas["IssueCreateCommand"]["description"] = (
    "При trip_id завершённой собственной поездки и inspection_id=null создаётся отдельное "
    "замечание stage=post_return. target_id — vehicle_id; expected_version — текущая версия "
    "машины. Владение поездкой проверяет сервер. Завершённый trip, after-inspection "
    "и их версии не меняются; машина получает needs_review и блокируется для новой выдачи."
)

schemas["Command"] = {
    "oneOf": command_refs,
    "discriminator": {"propertyName": "operation", "mapping": {
        operation: REF + "".join(part.capitalize() for part in operation.replace(".", "_").split("_")) + "Command"
        for operation, _, _, _ in command_specs
    }},
    "description": "Каждая операция имеет собственный payload. expected_version относится к target_id; чужой/устаревший объект не изменяется.",
}
schemas["CommandResult"] = obj({
    "operation": string(enum=[entry[0] for entry in command_specs]),
    "aggregate": {"oneOf": [ref(name) for name in (
        "Checkout", "Return", "Trip", "Inspection", "Issue", "Vehicle", "Employee",
        "Challenge", "Conversation")],
        "description": "Полное подтверждённое состояние объекта, соответствующего операции."},
    "correct": nullable({"type": "boolean"}),
    "attempts_remaining": nullable(integer(0, 3)),
    "challenge_proof_id": {**nullable(ref("UUID")),
                            "description": "UUID успешно решённого admin challenge; тот же ID команда передаёт как challenge_id. Mock/Python сверяют SHA-256 полного canonical intent и потребляют proof один раз."},
}, ("operation", "aggregate", "correct", "attempts_remaining", "challenge_proof_id"))
envelope("Command", ref("CommandResult"))

spec["paths"][P + "/commands"] = {"post": {
    "tags": ["commands"], "operationId": "executeCommand", "summary": "Выполнить бизнес-команду",
    "description": "Idempotency-Key scoped by actor, reused after timeout. Same key/different operation, target, version or payload → IDEMPOTENCY_CONFLICT. Domain, audit, outbox and result commit atomically. X-Inbox-* pair required for inbox-driven commands; lease fencing checked by Python. Map request uses trusted Go path after initData validation.",
    "security": [{"DataBearer": []}],
    "parameters": parameters(extra=[
        {"$ref": "#/components/parameters/IdempotencyKey"},
        {"name": "X-Inbox-Event-ID", "in": "header", "required": False, "schema": ref("UUID")},
        {"name": "X-Inbox-Lease", "in": "header", "required": False,
         "schema": string(200), "description": "Opaque fencing token; never log."},
    ]),
    "requestBody": {"required": True, "content": {"application/json": {"schema": ref("Command")}}},
    "responses": response(ref("CommandResponse")),
}}

spec["paths"][P + "/commands/{idempotency_key}"] = {"get": {
    "tags": ["commands"], "operationId": "getOwnCommandResult", "summary": "Получить результат своей команды",
    "description": "Только результат своей команды; всегда повторно проверить текущие права. Чужой ключ → NOT_FOUND.",
    "security": [{"DataBearer": []}],
    "parameters": parameters(extra=[
        {"name": "idempotency_key", "in": "path", "required": True,
         "schema": string(200)},
        query("operation", string(enum=[entry[0] for entry in command_specs]), True),
    ]),
    "responses": response(ref("CommandResponse")),
}}

schemas["InspectionPhotoUpload"] = obj({
    "image": {"type": "string", "format": "binary"},
    "expected_version": ref("Version"), "source_event_key": string(200),
}, ("image", "expected_version", "source_event_key"),
    "Single JPEG/PNG/WebP, максимум 10 MiB и 25 MP. MIME, сигнатура, декодирование и SHA-256 проверяются Python. Один slot 1…8; duplicate SHA в том же осмотре отклоняется; замена не стирает остальные 7.")
schemas["StagePhotoUpload"] = obj({
    "image": {"type": "string", "format": "binary"},
    "purpose": {"const": "issue"},
    "scope_type": string(enum=["vehicle", "trip", "inspection"]),
    "scope_id": ref("UUID"), "source_event_key": string(200),
}, ("image", "purpose", "scope_type", "scope_id", "source_event_key"))


def upload(path, operation_id, body_name, result_name, *, id_path=False, slot_path=False):
    spec["paths"][path] = {"post": {
        "tags": ["photos"], "operationId": operation_id, "summary": operation_id,
        "security": [{"DataBearer": []}],
        "parameters": parameters(id_path=id_path, slot_path=slot_path,
                                 extra=[{"$ref": "#/components/parameters/IdempotencyKey"}]),
        "requestBody": {"required": True, "content": {
            "multipart/form-data": {"schema": ref(body_name)}}},
        "responses": response(ref(result_name + "Response")),
    }}


upload(P + "/inspections/{id}/photos/{slot}", "uploadInspectionPhoto",
       "InspectionPhotoUpload", "PhotoUploadResult", id_path=True, slot_path=True)
upload(P + "/assets/stage", "stageIssueAsset", "StagePhotoUpload", "StagedAsset")


def binary_read(path, operation_id, *, slot_path=False, phase_path=False, description=""):
    spec["paths"][path] = {"get": {
        "tags": ["photos"], "operationId": operation_id, "summary": operation_id,
        "description": description,
        "security": [{"DataBearer": []}],
        "parameters": parameters(id_path=True, slot_path=slot_path,
                                 extra=[{"$ref": "#/components/parameters/PathPhotoPhase"}] if phase_path else []),
        "responses": {**response(ref("ErrorResponse")),
                      "200": {"description": "Авторизованный поток, без публичного URL",
                              "content": {"image/jpeg": {"schema": {"type": "string", "format": "binary"}},
                                          "image/png": {"schema": {"type": "string", "format": "binary"}},
                                          "image/webp": {"schema": {"type": "string", "format": "binary"}}}}},
    }}


binary_read(P + "/assets/{id}/content", "getAuthorizedAsset",
            description="Владелец/admin и контекст доступа проверяются для каждого запроса; чужой asset → NOT_FOUND.")
binary_read(P + "/vehicles/{id}/previous-inspection/photos/{slot}",
            "getAnonymizedPreviousPhoto", slot_path=True,
            description="Только последний finalized after-осмотр машины; этот маршрут не открывает чужую поездку или произвольный asset.")
binary_read(P + "/trips/{id}/inspection-photos/{phase}/{slot}",
            "getTripInspectionPhoto", slot_path=True, phase_path=True,
            description="Только владелец trip или admin. before доступно после finalized осмотра; after — лишь у завершённой или закрытой поездки с finalized after-осмотром. Чужая поездка, отсутствующий ракурс и ещё не доступная фаза дают одинаковый NOT_FOUND без asset ID и публичного URL.")


spec["info"]["description"] += (
    " Версии: vehicle растёт при смене доступности, блокировке, issue и коррекции; "
    "checkout при изменении оформления; inspection при ответе, фото и подтверждении; "
    "return при месте/отмене/завершении; trip при начале возврата, отмене, завершении и admin close; "
    "conversation только при save. Фото повышает inspection и его родительский checkout/return version. "
    "Перед start/complete Go читает актуальный агрегат. Hold равен 15 минутам серверного времени. "
    "Finalized inspection неизменяем, admin close не подставляет отсутствующие данные."
)


schemas["NormalizedEvent"] = obj({
    "integration_key": string(100), "event_key": string(200),
    "event_type": string(enum=["bot_started", "message_created", "message_callback"]),
    "actor_max_user_id": ref("MaxID"), "chat_id": ref("MaxID"),
    "message_id": nullable(string(200)), "callback_id": nullable(string(200)),
    "occurred_at": ref("Timestamp"),
    "payload": obj({
        "kind": string(enum=["start", "text", "photo", "geo", "callback"]),
        "text": nullable(string(1000)), "callback_data": nullable(string(200)),
        "photo_source_key": nullable(string(500)),
        "latitude": nullable({"type": "number", "minimum": -90, "maximum": 90}),
        "longitude": nullable({"type": "number", "minimum": -180, "maximum": 180}),
        "attachment_count": integer(0, 100),
    }, ("kind", "text", "callback_data", "photo_source_key", "latitude",
        "longitude", "attachment_count")),
}, ("integration_key", "event_key", "event_type", "actor_max_user_id",
    "chat_id", "message_id", "callback_id", "occurred_at", "payload"),
    "Go нормализует только личный чат и поддержанные события. event_key = message:<id>:<type> либо callback:<id>:<type>; для остальных — SHA-256 канонических нормализованных полей. Уникальность по integration_key+event_key. Содержимое фото в очередь не кладётся.")

schemas["InboxStored"] = obj({
    "id": ref("UUID"), "duplicate": {"type": "boolean"},
    "stored_at": ref("Timestamp"),
}, ("id", "duplicate", "stored_at"))
schemas["InboxClaimRequest"] = obj({
    "worker_id": string(100), "max_items": integer(1, 50),
}, ("worker_id", "max_items"))
schemas["InboxLease"] = obj({
    "id": ref("UUID"), "event": ref("NormalizedEvent"),
    "lease_token": string(200), "lease_expires_at": ref("Timestamp"),
    "attempt": integer(1),
}, ("id", "event", "lease_token", "lease_expires_at", "attempt"),
    "Один actor обрабатывается последовательно. Истёкший lease/token не даёт выполнить бизнес-команду.")
schemas["InboxClaim"] = obj({
    "items": array(ref("InboxLease"), 50),
}, ("items",))
schemas["LeaseAck"] = obj({
    "lease_token": string(200),
}, ("lease_token",))
schemas["InboxRetry"] = obj({
    "lease_token": string(200), "error_code": string(80),
    "next_attempt_at": ref("Timestamp"),
}, ("lease_token", "error_code", "next_attempt_at"))
schemas["QueueTransition"] = obj({
    "id": ref("UUID"), "state": string(enum=["done", "retry", "dead", "sent"]),
    "updated_at": ref("Timestamp"),
}, ("id", "state", "updated_at"))
schemas["Integration"] = obj({
    "key": string(100), "mode": string(enum=["webhook", "polling"]),
    "marker": nullable(string(500)), "lease_expires_at": nullable(ref("Timestamp")),
    "version": ref("Version"), "updated_at": ref("Timestamp"),
}, ("key", "mode", "marker", "lease_expires_at", "version", "updated_at"))
schemas["IntegrationLeaseRequest"] = obj({
    "worker_id": string(100), "expected_version": ref("Version"),
}, ("worker_id", "expected_version"))
schemas["IntegrationLease"] = obj({
    "lease_token": string(200), "lease_expires_at": ref("Timestamp"),
    "integration": ref("Integration"),
}, ("lease_token", "lease_expires_at", "integration"))
schemas["IntegrationCheckpoint"] = obj({
    "lease_token": string(200), "expected_version": ref("Version"),
    "previous_marker": nullable(string(500)), "new_marker": string(500),
    "stored_event_ids": array(ref("UUID"), 50),
}, ("lease_token", "expected_version", "previous_marker", "new_marker", "stored_event_ids"),
    "Marker продвигается CAS только после durable записи всей пачки событий.")
schemas["NotificationClaimRequest"] = obj({
    "worker_id": string(100), "max_items": integer(1, 50),
}, ("worker_id", "max_items"))
schemas["NotificationEvent"] = obj({
    "type": string(enum=["trip_started", "trip_completed", "trip_admin_closed",
                         "issue_created", "issue_resolved", "vehicle_blocked",
                         "access_changed"]),
    "resource_id": ref("UUID"), "vehicle_id": nullable(ref("UUID")),
    "reason": nullable(string(1000)), "occurred_at": ref("Timestamp"),
}, ("type", "resource_id", "vehicle_id", "reason", "occurred_at"))
schemas["NotificationLease"] = obj({
    "delivery_id": ref("UUID"), "event": ref("NotificationEvent"),
    "recipient_max_user_id": ref("MaxID"),
    "lease_token": string(200), "lease_expires_at": ref("Timestamp"),
    "attempt": integer(1),
}, ("delivery_id", "event", "recipient_max_user_id", "lease_token",
    "lease_expires_at", "attempt"))
schemas["NotificationClaim"] = obj({
    "items": array(ref("NotificationLease"), 50),
}, ("items",))
schemas["NotificationAck"] = obj({
    "lease_token": string(200), "provider_message_id": string(200),
}, ("lease_token", "provider_message_id"))
schemas["NotificationRetry"] = obj({
    "lease_token": string(200), "error_code": string(80),
    "retry_after": nullable(ref("Timestamp")),
    "dead": {"type": "boolean"},
}, ("lease_token", "error_code", "retry_after", "dead"))

for name in ("InboxStored", "InboxClaim", "QueueTransition", "Integration",
             "IntegrationLease", "NotificationClaim"):
    envelope(name, ref(name))


def worker_params(*, path_id=False, integration_key=False, mutation=False):
    result = parameters(actor=False, id_path=path_id)
    if integration_key:
        result.append({"name": "key", "in": "path", "required": True,
                       "schema": string(100)})
    if mutation:
        result.append({"$ref": "#/components/parameters/IdempotencyKey"})
    return result


def worker_post(path, operation_id, body_name, result_name, *, path_id=False,
                integration_key=False, description=""):
    spec["paths"][path] = {"post": {
        "tags": ["inbox" if "/inbox" in path else
                 "integration" if "/integrations" in path else "notifications"],
        "operationId": operation_id, "summary": operation_id, "description": description,
        "security": [{"WorkerBearer": []}],
        "parameters": worker_params(path_id=path_id,
                                    integration_key=integration_key, mutation=True),
        "requestBody": {"required": True, "content": {
            "application/json": {"schema": ref(body_name)}}},
        "responses": response(ref(result_name + "Response")),
    }}


worker_post(P + "/inbox", "storeInboxEvent", "NormalizedEvent", "InboxStored",
            description="Durable commit до HTTP 200 MAX. Дубликат integration_key+event_key возвращает прежний ID. Если БД недоступна, 503.")
worker_post(P + "/inbox/claim", "claimInbox", "InboxClaimRequest", "InboxClaim",
            description="Повтор с тем же ключом возвращает ту же аренду, пока она действительна; другой actor может обрабатываться параллельно.")
worker_post(P + "/inbox/{id}/ack", "ackInbox", "LeaseAck", "QueueTransition",
            path_id=True, description="Только текущий lease_token переводит событие в done.")
worker_post(P + "/inbox/{id}/retry", "retryInbox", "InboxRetry", "QueueTransition",
            path_id=True, description="Просроченный lease не меняет запись; после лимита попыток dead.")

spec["paths"][P + "/integrations/{key}"] = {"get": {
    "tags": ["integration"], "operationId": "getIntegration", "summary": "Получить состояние интеграции",
    "security": [{"WorkerBearer": []}],
    "parameters": worker_params(integration_key=True),
    "responses": response(ref("IntegrationResponse")),
}}
worker_post(P + "/integrations/{key}/lease", "leaseIntegration",
            "IntegrationLeaseRequest", "IntegrationLease", integration_key=True,
            description="Единственный poller на integration_key. Выдача/продление lease атомарна.")
worker_post(P + "/integrations/{key}/checkpoint", "checkpointIntegration",
            "IntegrationCheckpoint", "Integration", integration_key=True,
            description="Проверить lease, previous_marker и expected_version; marker только после сохранения всей пачки.")
worker_post(P + "/notifications/claim", "claimNotifications",
            "NotificationClaimRequest", "NotificationClaim",
            description="At-least-once внешняя доставка. Повтор после потери ответа MAX может дать дубль сообщения, но не доменную команду.")
worker_post(P + "/notifications/{id}/ack", "ackNotification",
            "NotificationAck", "QueueTransition", path_id=True,
            description="sent только с текущим lease и provider_message_id.")
worker_post(P + "/notifications/{id}/retry", "retryNotification",
            "NotificationRetry", "QueueTransition", path_id=True,
            description="429/5xx/backoff или dead; бизнес-транзакция не откатывается.")

for name in ("live", "ready"):
    path = "/health/" + name
    spec["paths"][path] = {"get": {
        "tags": ["health"], "operationId": "health" + name.capitalize(),
        "summary": "Проверить " + name,
        "security": [],
        "responses": {"200": {"description": "Без секретов и внутренних адресов",
                              "content": {"application/json": {"schema": obj({
                                  "status": {"const": "ok"}}, ("status",))}}},
                      "503": {"description": "Не готов", "content": {
                          "application/json": {"schema": obj({
                              "status": {"const": "unavailable"}}, ("status",))}}}},
    }}


ROOT.joinpath("data-api.openapi.yaml").write_bytes(
    yaml.safe_dump(spec, allow_unicode=True, sort_keys=False, width=110).encode("utf-8")
)
