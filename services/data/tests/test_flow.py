"""Сквозные API-сценарии на PostgreSQL: доступ, взятие, поездка, возврат."""

from __future__ import annotations

from conftest import (
    ADMIN,
    BLOCKED,
    DRIVER,
    DRIVER2,
    UNKNOWN,
    V1,
    V2,
    Api,
    begin_return,
    fill_return,
    start_trip,
    take_until_inspection,
)


def test_health_and_auth(api: Api) -> None:
    assert api.client.get("/health/live").json() == {"status": "ok"}
    ready = api.client.get("/health/ready")
    assert ready.status_code == 200, ready.text
    bad = api.client.get("/internal/v1/me", headers={**api.headers(DRIVER), "Authorization": "Bearer nope"})
    assert bad.status_code == 401 and bad.json()["error"]["code"] == "INVALID_SERVICE_TOKEN"
    wrong_kind = api.client.get("/internal/v1/me", headers=api.headers(DRIVER, worker=True))
    assert wrong_kind.status_code == 401
    no_version = api.headers(DRIVER)
    del no_version["X-Contract-Version"]
    assert api.client.get("/internal/v1/me", headers=no_version).status_code == 400
    meta = api.ok(api.get("/meta", None))
    assert meta["contract_version"] == "1.13" and meta["mode"] == "real"


def test_identity(api: Api) -> None:
    me = api.ok(api.get("/me", DRIVER))
    assert me["allowed"] is True and me["employee"]["role"] == "employee"
    unknown = api.ok(api.get("/me", UNKNOWN))
    assert unknown == {"allowed": False, "max_user_id": UNKNOWN, "employee": None}
    denied = api.get("/vehicles", UNKNOWN)  # AC-01
    assert denied.status_code == 403 and denied.json()["error"]["code"] == "ACCESS_DENIED"
    assert api.get("/admin/summary", DRIVER).json()["error"]["code"] == "ACCESS_DENIED"
    summary = api.ok(api.get("/admin/summary", ADMIN))
    assert summary["available"] == 10 and summary["holding"] == 0


def test_vehicle_list_paging(api: Api) -> None:
    first = api.ok(api.get("/vehicles", DRIVER, available="true"))
    assert len(first["items"]) == 5 and first["next_cursor"]
    second = api.ok(api.get("/vehicles", DRIVER, available="true", cursor=first["next_cursor"]))
    assert len(second["items"]) == 5 and second["next_cursor"] is None
    assert {v["id"] for v in first["items"]}.isdisjoint({v["id"] for v in second["items"]})
    assert api.get("/vehicles", DRIVER, limit="51").status_code == 400
    assert api.get("/vehicles", DRIVER, limit="2", cursor=first["next_cursor"]).status_code == 400
    assert api.get("/vehicles", DRIVER, bogus="1").status_code == 400


def test_happy_path_take_and_return(api: Api) -> None:
    trip = start_trip(api, DRIVER, V1)  # AC-10
    assert trip["status"] == "active" and trip["before_inspection"]["status"] == "finalized"
    assert trip["before_inspection"]["missing_slots"] == []
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    assert vehicle["status"] == "in_trip"
    state = api.ok(api.get("/state", DRIVER))
    assert state["trip"]["id"] == trip["id"] and state["next_step"] == "active_trip"
    other = api.cmd(DRIVER, "checkout.create", V2, 1)  # AC-04
    assert other.status_code == 409 and other.json()["error"]["code"] == "USER_BUSY"

    ret = begin_return(api, DRIVER, trip)
    assert ret["step"] == "checklist" and ret["intent_confirmed_at"]
    ret = fill_return(api, DRIVER, ret)
    done = api.agg(api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True}))
    assert done["status"] == "completed"
    trip = api.ok(api.get(f"/trips/{trip['id']}", DRIVER))
    assert trip["status"] == "completed" and trip["after_inspection"]["status"] == "finalized"
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))  # AC-16
    assert vehicle["status"] == "available" and vehicle["current_fuel"] == 25
    assert vehicle["current_parking"]["source"] == "manual_map"
    assert vehicle["current_odometer_km"] == trip["after_inspection"]["odometer_km"]
    prev = api.ok(api.get(f"/vehicles/{V1}/previous-inspection", DRIVER2))
    assert prev["id"] == trip["after_inspection"]["id"]
    mine = api.ok(api.get("/trips", DRIVER, scope="mine"))
    assert [t["id"] for t in mine["items"]] == [trip["id"]]
    assert api.ok(api.get("/trips", DRIVER2, scope="mine"))["items"] == []
    assert api.get(f"/trips/{trip['id']}", DRIVER2).status_code == 404  # AC-21


def test_notifications_materialized(api: Api) -> None:
    start_trip(api, DRIVER, V1)
    claim = api.ok(api.worker("/notifications/claim", {"worker_id": "w1", "max_items": 10}))
    assert len(claim["items"]) == 1
    item = claim["items"][0]
    assert item["event"]["type"] == "trip_started" and item["recipient_max_user_id"] == ADMIN
    ack = api.ok(api.worker(f"/notifications/{item['delivery_id']}/ack",
                            {"lease_token": item["lease_token"], "provider_message_id": "mid-1"}))
    assert ack["state"] == "sent"


def test_blocked_driver_cannot_take(api: Api) -> None:
    r = api.cmd(BLOCKED, "checkout.create", V1, 1)
    assert r.status_code == 403 and r.json()["error"]["code"] == "CANNOT_START_TRIP"


def test_checkout_cancel_and_stale_version(api: Api) -> None:
    checkout = take_until_inspection(api, DRIVER, V1)
    stale = api.cmd(DRIVER, "checkout.cancel", checkout["id"], checkout["version"] - 1)
    assert stale.status_code == 409 and stale.json()["error"]["code"] == "STALE_VERSION"
    assert stale.json()["error"]["details"]["current_version"] == checkout["version"]
    cancelled = api.agg(api.cmd(DRIVER, "checkout.cancel", checkout["id"], checkout["version"]))
    assert cancelled["status"] == "cancelled"
    assert api.ok(api.get(f"/vehicles/{V1}", DRIVER))["status"] == "available"
