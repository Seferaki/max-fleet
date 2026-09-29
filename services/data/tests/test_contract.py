"""Ответы Python валидируются JSON Schema из contracts/data-api.openapi.yaml.

Go декодирует ответы строго (DisallowUnknownFields), поэтому лишнее или
пропущенное поле ломает интеграцию — проверяем каждый тип ответа.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest
import yaml
from jsonschema import Draft202012Validator
from referencing import Registry, Resource

from conftest import ADMIN, DRIVER, UNKNOWN, V1, V2, Api, begin_return, fill_return, start_trip, take_until_inspection
from test_admin_queues import _event, admin_proof

SPEC_PATH = Path(__file__).resolve().parents[3] / "contracts" / "data-api.openapi.yaml"


@pytest.fixture(scope="module")
def spec() -> dict[str, Any]:
    if not SPEC_PATH.exists():
        pytest.skip("contracts/ недоступен (запуск вне репозитория)")
    return yaml.safe_load(SPEC_PATH.read_text(encoding="utf-8"))


def check(spec: dict[str, Any], schema_name: str, body: Any) -> None:
    root = "urn:max-fleet:data-api"
    registry = Registry().with_resource(root, Resource.opaque(spec))
    validator = Draft202012Validator({"$ref": f"{root}#/components/schemas/{schema_name}"}, registry=registry)
    errors = sorted(validator.iter_errors(body), key=lambda e: list(e.path))
    assert not errors, f"{schema_name}: " + "; ".join(f"{list(e.path)}: {e.message}" for e in errors[:5])


def test_read_responses_match_contract(api: Api, spec: dict[str, Any]) -> None:
    check(spec, "MetaResponse", api.get("/meta", None).json())
    check(spec, "MeResponse", api.get("/me", DRIVER).json())
    check(spec, "MeResponse", api.get("/me", UNKNOWN).json())
    check(spec, "RulesResponse", api.get("/rules/current", DRIVER).json())
    check(spec, "VehiclePageResponse", api.get("/vehicles", DRIVER, available="true").json())
    check(spec, "VehicleResponse", api.get(f"/vehicles/{V1}", DRIVER).json())
    check(spec, "CurrentStateResponse", api.get("/state", DRIVER).json())
    check(spec, "AdminSummaryResponse", api.get("/admin/summary", ADMIN).json())
    check(spec, "EmployeePageResponse", api.get("/admin/employees", ADMIN).json())
    check(spec, "ErrorResponse", api.get("/vehicles", UNKNOWN).json())
    check(spec, "ErrorResponse", api.get(f"/vehicles/{V1}/previous-inspection", DRIVER).json())


def test_full_cycle_responses_match_contract(api: Api, spec: dict[str, Any]) -> None:
    checkout = take_until_inspection(api, DRIVER, V2)
    check(spec, "CheckoutResponse", api.get(f"/checkouts/{checkout['id']}", DRIVER).json())
    check(spec, "InspectionResponse", api.get(f"/inspections/{checkout['inspection']['id']}", DRIVER).json())
    check(spec, "CommandResponse", api.cmd(DRIVER, "checkout.cancel", checkout["id"], checkout["version"]).json())

    trip = start_trip(api, DRIVER, V1)
    state = api.get("/state", DRIVER).json()
    check(spec, "CurrentStateResponse", state)
    check(spec, "TripResponse", api.get(f"/trips/{trip['id']}", DRIVER).json())
    ret = begin_return(api, DRIVER, trip)
    check(spec, "ReturnResponse", api.get(f"/returns/{ret['id']}", DRIVER).json())
    check(spec, "CurrentStateResponse", api.get("/state", DRIVER).json())
    ret = fill_return(api, DRIVER, ret)
    upload = api.upload(DRIVER, ret["inspection"]["id"], 1, ret["inspection"]["version"], b"x")
    check(spec, "ErrorResponse", upload.json())
    stale = api.cmd(DRIVER, "return.complete", ret["id"], ret["version"] - 1, {"attestation": True})
    check(spec, "ErrorResponse", stale.json())
    done = api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True})
    check(spec, "CommandResponse", done.json())
    check(spec, "TripPageResponse", api.get("/trips", DRIVER, scope="mine").json())
    check(spec, "TripPageResponse", api.get("/admin/trips", ADMIN, state="completed").json())
    check(spec, "InspectionResponse", api.get(f"/vehicles/{V1}/previous-inspection", DRIVER).json())

    trip2 = start_trip(api, DRIVER, V1)
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    issue = api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "other", "description": "Тест", "trip_id": trip2["id"], "asset_ids": []})
    check(spec, "CommandResponse", issue.json())
    check(spec, "IssueResponse", api.get(f"/issues/{issue.json()['data']['aggregate']['id']}", ADMIN).json())
    check(spec, "IssuePageResponse", api.get("/admin/issues", ADMIN).json())
    trip2 = api.ok(api.get(f"/trips/{trip2['id']}", ADMIN))
    proof = admin_proof(api, "admin_close", trip2["id"], trip2["version"], reason="Тест")
    closed = api.cmd(ADMIN, "trip.admin_close", trip2["id"], trip2["version"],
                     {"reason": "Тест", "challenge_id": proof})
    check(spec, "CommandResponse", closed.json())
    emp = api.ok(api.get("/me", DRIVER))["employee"]
    check(spec, "EmployeeResponse", api.get(f"/admin/employees/{emp['id']}", ADMIN).json())
    conv = api.cmd(DRIVER, "conversation.save", emp["id"], 1, {
        "flow": "issue_admin_resolution", "step": "confirm",
        "context": {"vehicle_id": V1, "issue_version": 2, "trip_version": 3, "issue_category": "parking",
                    "asset_ids": [], "vehicle_version": 4, "correction_odometer_km": 5000,
                    "challenge_version": 1, "challenge_question": "2 + 2 = ?",
                    "challenge_options": [2, 3, 4, 5], "challenge_expires_at": "2026-09-30T10:00:00Z"},
        "pending_input_kind": None})
    check(spec, "CommandResponse", conv.json())
    check(spec, "CurrentStateResponse", api.get("/state", DRIVER).json())


def test_worker_responses_match_contract(api: Api, spec: dict[str, Any]) -> None:
    check(spec, "InboxStoredResponse", api.worker("/inbox", _event("k1")).json())
    claim = api.worker("/inbox/claim", {"worker_id": "w", "max_items": 5}).json()
    check(spec, "InboxClaimResponse", claim)
    item = claim["data"]["items"][0]
    check(spec, "QueueTransitionResponse",
          api.worker(f"/inbox/{item['id']}/ack", {"lease_token": item["lease_token"]}).json())
    check(spec, "IntegrationResponse", api.client.get("/internal/v1/integrations/demo-bot",
                                                      headers=api.headers(None, worker=True)).json())
    check(spec, "IntegrationLeaseResponse",
          api.worker("/integrations/demo-bot/lease", {"worker_id": "p", "expected_version": 1}).json())
    start_trip(api, DRIVER, V1)
    claim = api.worker("/notifications/claim", {"worker_id": "w", "max_items": 5}).json()
    check(spec, "NotificationClaimResponse", claim)
    check(spec, "ErrorResponse", api.worker("/inbox/claim", {"worker_id": "w"}).json())
