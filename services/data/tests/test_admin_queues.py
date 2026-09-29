"""Администрирование (AC-22…AC-25) и очереди inbox/notifications/integration."""

from __future__ import annotations

from typing import Any

from conftest import (
    ADMIN,
    BLOCKED,
    DRIVER,
    DRIVER2,
    V1,
    V2,
    Api,
    begin_return,
    fill_return,
    solve,
    start_trip,
    take_until_inspection,
)


def admin_proof(api: Api, purpose: str, target: str | None, version: int | None, **fields: Any) -> str:
    operation = {"vehicle_block": "vehicle.block", "vehicle_unblock": "vehicle.unblock",
                 "employee_grant": "employee.grant", "employee_access": "employee.access",
                 "admin_close": "trip.admin_close"}[purpose]
    intent = {"operation": operation, "target_id": target, "expected_version": version, **fields}
    ch = api.agg(api.cmd(ADMIN, "challenge.create", target, version, {"purpose": purpose,
                                                                        "intent_payload": intent}))
    answer = solve(api, ADMIN, ch)
    assert answer["correct"] and answer["challenge_proof_id"] == ch["id"]
    return ch["id"]


def test_block_during_trip_and_unblock(api: Api) -> None:
    """AC-22: блокировка машины в поездке не завершает поездку."""
    trip = start_trip(api, DRIVER, V1)
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    proof = admin_proof(api, "vehicle_block", V1, vehicle["version"], reason="Проверка тормозов")
    denied = api.cmd(DRIVER, "vehicle.block", V1, vehicle["version"],
                     {"reason": "Проверка тормозов", "challenge_id": proof})
    assert denied.status_code == 403 and denied.json()["error"]["code"] == "ADMIN_REQUIRED"
    tampered = api.cmd(ADMIN, "vehicle.block", V1, vehicle["version"],
                       {"reason": "Другая причина", "challenge_id": proof})
    assert tampered.status_code == 422 and tampered.json()["error"]["code"] == "CHALLENGE_INVALID"
    blocked = api.agg(api.cmd(ADMIN, "vehicle.block", V1, vehicle["version"],
                              {"reason": "Проверка тормозов", "challenge_id": proof}))
    assert blocked["manual_blocked"] and blocked["status"] == "in_trip"
    reuse = api.cmd(ADMIN, "vehicle.block", V1, blocked["version"],
                    {"reason": "Проверка тормозов", "challenge_id": proof})
    assert reuse.status_code in (409, 422)
    assert api.ok(api.get(f"/trips/{trip['id']}", DRIVER))["status"] == "active"
    ret = fill_return(api, DRIVER, begin_return(api, DRIVER, trip))
    api.agg(api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True}))
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    assert vehicle["status"] == "unavailable"
    proof = admin_proof(api, "vehicle_unblock", V1, vehicle["version"], reason="Исправлено",
                        review_completed=True)
    unblocked = api.agg(api.cmd(ADMIN, "vehicle.unblock", V1, vehicle["version"], {
        "reason": "Исправлено", "review_completed": True, "challenge_id": proof}))
    assert unblocked["status"] == "available" and not unblocked["manual_blocked"]


def test_block_cancels_hold(api: Api) -> None:
    checkout = take_until_inspection(api, DRIVER, V2)
    vehicle = api.ok(api.get(f"/vehicles/{V2}", ADMIN))
    proof = admin_proof(api, "vehicle_block", V2, vehicle["version"], reason="ДТП")
    api.agg(api.cmd(ADMIN, "vehicle.block", V2, vehicle["version"], {"reason": "ДТП", "challenge_id": proof}))
    assert api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))["status"] == "cancelled"
    assert api.ok(api.get("/state", DRIVER))["checkout"] is None


def test_admin_close_incomplete_data(api: Api) -> None:
    """AC-23: аварийное закрытие без выдуманных данных."""
    trip = start_trip(api, DRIVER, V1)
    proof = admin_proof(api, "admin_close", trip["id"], trip["version"], reason="Телефон разбит")
    closed = api.agg(api.cmd(ADMIN, "trip.admin_close", trip["id"], trip["version"], {
        "reason": "Телефон разбит", "challenge_id": proof, "available_data": {"fuel_level": 50}}))
    assert closed["status"] == "closed_by_admin" and closed["ended_at"]
    assert set(closed["missing_data"]) == {"after_photos", "odometer_km", "parking_location",
                                           "keys_returned", "car_locked"}
    assert closed["after_inspection"] is None
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    assert vehicle["needs_review"] and vehicle["status"] == "unavailable"
    assert api.ok(api.get("/state", DRIVER))["trip"] is None
    claim = api.ok(api.worker("/notifications/claim", {"worker_id": "w", "max_items": 10}))
    types = sorted((i["event"]["type"], i["recipient_max_user_id"]) for i in claim["items"])
    assert ("trip_admin_closed", DRIVER) in types


def test_employee_access_and_grant(api: Api) -> None:
    """AC-25: отозванный доступ не мешает вернуть свою машину."""
    trip = start_trip(api, DRIVER, V1)
    emp = api.ok(api.get("/me", DRIVER))["employee"]
    proof = admin_proof(api, "employee_access", emp["id"], emp["version"], can_start_trip=False,
                        reason="Уволен")
    updated = api.agg(api.cmd(ADMIN, "employee.access", emp["id"], emp["version"], {
        "can_start_trip": False, "reason": "Уволен", "challenge_id": proof}))
    assert updated["can_start_trip"] is False and updated["active_trip_id"] == trip["id"]
    ret = fill_return(api, DRIVER, begin_return(api, DRIVER, trip))
    done = api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True})
    assert done.status_code == 200
    again = api.cmd(DRIVER, "checkout.create", V2, 1)
    assert again.json()["error"]["code"] == "CANNOT_START_TRIP"
    proof = admin_proof(api, "employee_grant", None, None, max_user_id="8000000000000000055",
                        display_name="Новый сотрудник")
    granted = api.agg(api.cmd(ADMIN, "employee.grant", None, None, {
        "max_user_id": "8000000000000000055", "display_name": "Новый сотрудник", "challenge_id": proof}))
    assert granted["role"] == "employee" and granted["max_user_id"] == "8000000000000000055"
    assert api.ok(api.get("/me", "8000000000000000055"))["allowed"] is True
    employees = api.ok(api.get("/admin/employees", ADMIN, limit="50"))
    assert len(employees["items"]) == 5
    assert api.get("/admin/employees", BLOCKED).status_code == 403


def test_issue_resolution_unblocks(api: Api) -> None:
    trip = start_trip(api, DRIVER, V1)
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    issue = api.agg(api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "mechanical", "description": "Горит check engine", "trip_id": trip["id"], "asset_ids": []}))
    assert issue["stage"] == "during"
    trip = api.ok(api.get(f"/trips/{trip['id']}", DRIVER))
    assert trip["status"] == "active" and trip["version"] == 2  # замечание повышает версию поездки
    ret = fill_return(api, DRIVER, begin_return(api, DRIVER, trip))
    api.agg(api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True}))
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    assert vehicle["status"] == "unavailable"
    still_blocking = api.cmd(ADMIN, "vehicle.unblock", V1, vehicle["version"], {
        "reason": "x", "review_completed": True, "challenge_id": admin_proof(
            api, "vehicle_unblock", V1, vehicle["version"], reason="x", review_completed=True)})
    assert still_blocking.status_code == 409
    resolved = api.agg(api.cmd(ADMIN, "issue.resolve", issue["id"], issue["version"], {
        "status": "known_nonblocking", "comment": "Датчик, не влияет", "confirmation": True}))
    assert resolved["status"] == "known_nonblocking" and not resolved["blocks_issuance"]
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    proof = admin_proof(api, "vehicle_unblock", V1, vehicle["version"], reason="Проверено", review_completed=True)
    vehicle = api.agg(api.cmd(ADMIN, "vehicle.unblock", V1, vehicle["version"], {
        "reason": "Проверено", "review_completed": True, "challenge_id": proof}))
    assert vehicle["status"] == "available" and vehicle["known_nonblocking_issues"] == [issue["id"]]
    assert api.get(f"/issues/{issue['id']}", DRIVER2).status_code == 404


def test_post_return_issue_categories_and_assignment(api: Api) -> None:
    trip = start_trip(api, DRIVER, V1)
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    trip_version = trip["version"]
    corrected_vehicle = api.agg(api.cmd(ADMIN, "vehicle.correct_snapshot", V1, vehicle["version"], {
        "reason": "Сверка одометра во время поездки", "odometer_km": vehicle["current_odometer_km"],
        "confirmation": True}))
    assert corrected_vehicle["current_odometer_km"] == vehicle["current_odometer_km"]
    assert api.ok(api.get(f"/trips/{trip['id']}", DRIVER))["version"] == trip_version
    ret = fill_return(api, DRIVER, begin_return(api, DRIVER, trip))
    api.ok(api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True}))
    finished = api.ok(api.get(f"/trips/{trip['id']}", DRIVER))
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    parking_issue = api.agg(api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "parking", "description": "Не удалось безопасно припарковать", "trip_id": trip["id"],
        "asset_ids": []}))
    assert parking_issue["stage"] == "post_return" and parking_issue["assigned_to"] is None
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    lock_issue = api.agg(api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "car_lock", "description": "Машина не закрывается", "trip_id": trip["id"], "asset_ids": []}))
    assert lock_issue["category"] == "car_lock" and lock_issue["stage"] == "post_return"
    assert api.ok(api.get(f"/trips/{trip['id']}", DRIVER))["version"] == finished["version"]

    admin_id = api.ok(api.get("/me", ADMIN))["employee"]["id"]
    denied = api.cmd(DRIVER, "issue.resolve", parking_issue["id"], parking_issue["version"], {
        "status": "in_progress", "comment": "Начата проверка", "confirmation": True})
    assert denied.status_code == 403
    assigned = api.agg(api.cmd(ADMIN, "issue.resolve", parking_issue["id"], parking_issue["version"], {
        "status": "in_progress", "comment": "Начата проверка", "confirmation": True}))
    assert assigned["assigned_to"] == admin_id
    resolved = api.agg(api.cmd(ADMIN, "issue.resolve", parking_issue["id"], assigned["version"], {
        "status": "resolved", "comment": "Устранено", "confirmation": True}))
    assert resolved["assigned_to"] == admin_id and resolved["resolved_by"] == admin_id


def _event(key: str, actor: str = DRIVER, text: str = "hi") -> dict[str, Any]:
    return {"integration_key": "demo-bot", "event_key": f"message:{key}:message_created",
            "event_type": "message_created", "actor_max_user_id": actor, "chat_id": actor,
            "message_id": key, "callback_id": None, "occurred_at": "2026-09-28T10:00:00Z",
            "payload": {"kind": "text", "text": text, "callback_data": None, "photo_source_key": None,
                        "latitude": None, "longitude": None, "attachment_count": 0}}


def test_inbox_order_lease_and_fencing(api: Api) -> None:
    first = api.ok(api.worker("/inbox", _event("m1"), key="inbox-key-1"))
    assert first["duplicate"] is False
    dup = api.ok(api.worker("/inbox", _event("m1"), key="inbox-key-2"))
    assert dup["duplicate"] is True and dup["id"] == first["id"]
    conflict = api.worker("/inbox", _event("m1", text="other"), key="inbox-key-3")
    assert conflict.status_code == 409
    api.ok(api.worker("/inbox", _event("m2"), key="inbox-key-4"))
    api.ok(api.worker("/inbox", _event("x1", actor=DRIVER2), key="inbox-key-5"))
    claim = api.ok(api.worker("/inbox/claim", {"worker_id": "w1", "max_items": 10}, key="claim-key-1"))
    ids = [item["event"]["event_key"] for item in claim["items"]]
    assert ids == ["message:m1:message_created", "message:x1:message_created"]  # один на actor
    replay = api.ok(api.worker("/inbox/claim", {"worker_id": "w1", "max_items": 10}, key="claim-key-1"))
    assert replay == claim
    item = claim["items"][0]
    stale_ack = api.worker(f"/inbox/{item['id']}/ack", {"lease_token": "wrong"})
    assert stale_ack.status_code == 409 and stale_ack.json()["error"]["code"] == "LEASE_EXPIRED"
    fenced = api.client.post("/internal/v1/commands", headers={
        **api.headers(DRIVER, key="fenced-cmd-1"), "X-Inbox-Event-ID": item["id"], "X-Inbox-Lease": "wrong"},
        json={"operation": "checkout.create", "target_id": V1, "expected_version": 1, "payload": {}})
    assert fenced.status_code == 409 and fenced.json()["error"]["code"] == "LEASE_EXPIRED"
    leased = api.client.post("/internal/v1/commands", headers={
        **api.headers(DRIVER, key="fenced-cmd-2"), "X-Inbox-Event-ID": item["id"],
        "X-Inbox-Lease": item["lease_token"]},
        json={"operation": "checkout.create", "target_id": V1, "expected_version": 1, "payload": {}})
    assert leased.status_code == 200, leased.text
    done = api.ok(api.worker(f"/inbox/{item['id']}/ack", {"lease_token": item["lease_token"]}))
    assert done["state"] == "done"
    nxt = api.ok(api.worker("/inbox/claim", {"worker_id": "w1", "max_items": 10}))
    assert [i["event"]["event_key"] for i in nxt["items"]] == ["message:m2:message_created"]
    retry = api.ok(api.worker(f"/inbox/{nxt['items'][0]['id']}/retry", {
        "lease_token": nxt["items"][0]["lease_token"], "error_code": "MAX_TIMEOUT",
        "next_attempt_at": "2099-01-01T00:00:00Z"}))
    assert retry["state"] == "retry"
    assert api.ok(api.worker("/inbox/claim", {"worker_id": "w1", "max_items": 10}))["items"] == []


def test_integration_lease_and_checkpoint(api: Api) -> None:
    info = api.ok(api.client.get("/internal/v1/integrations/demo-bot", headers=api.headers(None, worker=True)))
    assert info["mode"] == "polling" and info["marker"] is None
    lease = api.ok(api.worker("/integrations/demo-bot/lease", {"worker_id": "p1", "expected_version": 1}))
    other = api.worker("/integrations/demo-bot/lease",
                       {"worker_id": "p2", "expected_version": lease["integration"]["version"]})
    assert other.status_code == 409 and other.json()["error"]["code"] == "COMMAND_IN_PROGRESS"
    stored = api.ok(api.worker("/inbox", _event("c1")))
    cp = api.ok(api.worker("/integrations/demo-bot/checkpoint", {
        "lease_token": lease["lease_token"], "expected_version": lease["integration"]["version"],
        "previous_marker": None, "new_marker": "marker-1", "stored_event_ids": [stored["id"]]}))
    assert cp["marker"] == "marker-1"
    stale = api.worker("/integrations/demo-bot/checkpoint", {
        "lease_token": lease["lease_token"], "expected_version": lease["integration"]["version"],
        "previous_marker": None, "new_marker": "marker-2", "stored_event_ids": []})
    assert stale.status_code == 409 and stale.json()["error"]["code"] == "STALE_VERSION"


def test_notification_retry_dead(api: Api) -> None:
    start_trip(api, DRIVER, V1)
    item = api.ok(api.worker("/notifications/claim", {"worker_id": "w", "max_items": 1}))["items"][0]
    bad = api.worker(f"/notifications/{item['delivery_id']}/retry", {
        "lease_token": item["lease_token"], "error_code": "MAX_429", "retry_after": None, "dead": False})
    assert bad.status_code == 400
    dead = api.ok(api.worker(f"/notifications/{item['delivery_id']}/retry", {
        "lease_token": item["lease_token"], "error_code": "BOT_BLOCKED", "retry_after": None, "dead": True}))
    assert dead["state"] == "dead"
    trip = api.ok(api.get("/state", DRIVER))["trip"]
    assert trip["status"] == "active"  # AC-24: доставка не откатывает поездку
