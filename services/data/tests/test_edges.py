"""Оставшиеся сценарии DE-04…DE-07: гонки блокировки, отказ БД после S3, лимиты, lease."""

from __future__ import annotations

import threading
from concurrent.futures import ThreadPoolExecutor

import pytest
from sqlalchemy import text

from conftest import (
    ADMIN,
    DRIVER,
    V1,
    Api,
    png,
    take_until_inspection,
    upload_all,
)
from test_admin_queues import _event, admin_proof


def _ready_to_start(api: Api) -> dict:
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    insp = api.agg(api.cmd(DRIVER, "inspection.update", insp["id"], insp["version"],
                           {"fuel_level": 50, "odometer_km": 20000}))
    insp = upload_all(api, DRIVER, insp)
    api.agg(api.cmd(DRIVER, "inspection.confirm_photos", insp["id"], insp["version"]))
    checkout = api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))
    return api.agg(api.cmd(DRIVER, "checkout.set_no_new_issues", checkout["id"], checkout["version"],
                           {"value": True}))


def _block_vs_start_once(api: Api, engine: object) -> None:
    checkout = _ready_to_start(api)
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    proof = admin_proof(api, "vehicle_block", V1, vehicle["version"], reason="Гонка")
    barrier = threading.Barrier(2)

    def start() -> int:
        barrier.wait()
        return api.cmd(DRIVER, "checkout.start", checkout["id"], checkout["version"],
                       {"attestation": True}).status_code

    def block() -> int:
        barrier.wait()
        return api.cmd(ADMIN, "vehicle.block", V1, vehicle["version"],
                       {"reason": "Гонка", "challenge_id": proof}).status_code

    with ThreadPoolExecutor(2) as pool:
        s, b = pool.submit(start), pool.submit(block)
        start_code, block_code = s.result(), b.result()
    final = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    with engine.connect() as conn:  # type: ignore[attr-defined]
        trips = conn.execute(text("SELECT count(*) FROM trips WHERE status='active'")).scalar_one()
    if start_code == 200:
        assert trips == 1 and final["status"] == "in_trip"
        assert block_code in (200, 409)  # блок после старта мог устареть по версии машины
    else:
        assert block_code == 200 and trips == 0 and final["status"] == "unavailable"
    with engine.begin() as conn:  # type: ignore[attr-defined]
        conn.execute(text("DELETE FROM vehicle_assignments"))
        conn.execute(text("UPDATE trips SET status='completed', ended_at=now() WHERE status='active'"))
        conn.execute(text("UPDATE vehicles SET manual_blocked=false, block_reason=null, blocked_by=null"))
        conn.execute(text("UPDATE checkout_attempts SET status='cancelled' WHERE status='holding'"))


def test_block_vs_start_race(api: Api, app_and_store: tuple) -> None:
    """Блокировка и старт одновременно: либо поездка (запрет следующей выдачи), либо отказ старта."""
    for _ in range(3):
        _block_vs_start_once(api, app_and_store[0].state.engine)


def test_db_failure_after_s3_keeps_previous_photo(api: Api, app_and_store: tuple, monkeypatch: pytest.MonkeyPatch,
                                                  store: object) -> None:
    from app.domain import photos

    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    first = api.ok(api.upload(DRIVER, insp["id"], 1, insp["version"], png()))
    original = photos.complete_idempotency

    def boom(*args: object, **kwargs: object) -> None:
        raise RuntimeError("db down after S3")

    monkeypatch.setattr(photos, "complete_idempotency", boom)
    from fastapi.testclient import TestClient

    tolerant = Api(TestClient(app_and_store[0], raise_server_exceptions=False))
    failed = tolerant.upload(DRIVER, insp["id"], 1, first["inspection"]["version"], png())
    assert failed.status_code == 503 and failed.json()["error"]["code"] == "TEMPORARY_FAILURE"
    monkeypatch.setattr(photos, "complete_idempotency", original)
    current = api.ok(api.get(f"/inspections/{insp['id']}", DRIVER))
    assert current["occupied_slots"] == [1] and current["version"] == first["inspection"]["version"]
    content = api.client.get(f"/internal/v1/assets/{first['asset_id']}/content", headers=api.headers(DRIVER))
    assert content.status_code == 200  # прежний снимок не потерян
    with app_and_store[0].state.engine.connect() as conn:
        staged = conn.execute(text("SELECT count(*) FROM photo_assets WHERE state='staged'")).scalar_one()
    assert staged == 1  # новый объект остался staged и будет убран cleanup


def test_issue_photo_limit_and_file_limits(api: Api, settings: object) -> None:
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    assets = []
    for i in range(4):
        r = api.client.post("/internal/v1/assets/stage", headers=api.headers(DRIVER, key=f"stage-limit-{i:03d}"),
                            data={"purpose": "issue", "scope_type": "inspection", "scope_id": insp["id"],
                                  "source_event_key": f"message:lim{i}:message_created"},
                            files={"image": ("p.png", png(), "image/png")})
        assert r.status_code == 200, r.text
        assets.append(r.json()["data"]["asset_id"])
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    too_many = api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "other", "description": "x", "inspection_id": insp["id"], "asset_ids": assets})
    assert too_many.status_code == 400
    big = api.upload(DRIVER, insp["id"], 1, insp["version"], b"\x89PNG" + b"\x00" * (10 * 1024 * 1024 + 10))
    assert big.status_code == 413 and big.json()["error"]["code"] == "FILE_TOO_LARGE"
    huge = api.upload(DRIVER, insp["id"], 1, insp["version"], png(size=(6000, 5000)))
    assert huge.status_code == 415  # 30 MP > 25 MP


def test_expired_lease_cannot_ack_after_takeover(api: Api, app_and_store: tuple) -> None:
    api.ok(api.worker("/inbox", _event("lease1")))
    old = api.ok(api.worker("/inbox/claim", {"worker_id": "old", "max_items": 1}))["items"][0]
    with app_and_store[0].state.engine.begin() as conn:
        conn.execute(text("UPDATE inbound_events SET lease_until = now() - interval '1 second'"))
    new = api.ok(api.worker("/inbox/claim", {"worker_id": "new", "max_items": 1}))["items"][0]
    assert new["id"] == old["id"] and new["lease_token"] != old["lease_token"] and new["attempt"] == 2
    stale = api.worker(f"/inbox/{old['id']}/ack", {"lease_token": old["lease_token"]})
    assert stale.status_code == 409 and stale.json()["error"]["code"] == "LEASE_EXPIRED"
    fenced = api.client.post("/internal/v1/commands", headers={
        **api.headers(DRIVER, key="fenced-old-1"), "X-Inbox-Event-ID": old["id"], "X-Inbox-Lease": old["lease_token"]},
        json={"operation": "checkout.create", "target_id": V1, "expected_version": 1, "payload": {}})
    assert fenced.status_code == 409
    assert api.ok(api.worker(f"/inbox/{new['id']}/ack", {"lease_token": new["lease_token"]}))["state"] == "done"


def test_admin_vehicle_edits(api: Api) -> None:
    vehicle = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    denied = api.cmd(DRIVER, "vehicle.edit", V1, vehicle["version"], {"description": "x", "confirmation": True})
    assert denied.status_code == 403
    edited = api.agg(api.cmd(ADMIN, "vehicle.edit", V1, vehicle["version"],
                             {"key_instructions": "Ключ в сейфе №2", "confirmation": True}))
    assert edited["key_instructions"] == "Ключ в сейфе №2" and edited["version"] == vehicle["version"] + 1
    corrected = api.agg(api.cmd(ADMIN, "vehicle.correct_snapshot", V1, edited["version"], {
        "reason": "Опечатка", "odometer_km": 5, "location": {
            "latitude": 55.7, "longitude": 37.6, "source": "admin", "confirmed": True}, "confirmation": True}))
    assert corrected["current_odometer_km"] == 5 and corrected["current_parking"]["source"] == "admin"
    annotated = api.agg(api.cmd(ADMIN, "vehicle.annotate", V1, corrected["version"],
                                {"reason": "Уточнение", "text": "Скол на стекле известен", "confirmation": True}))
    assert annotated["version"] == corrected["version"] + 1
    checkout = take_until_inspection(api, DRIVER, V1)
    busy = api.ok(api.get(f"/vehicles/{V1}", ADMIN))
    held = api.cmd(ADMIN, "vehicle.correct_snapshot", V1, busy["version"],
                   {"reason": "x", "fuel_level": 100, "confirmation": True})
    assert held.status_code == 409  # при активной брони можно менять только одометр
    denied = api.cmd(DRIVER, "vehicle.correct_snapshot", V1, busy["version"],
                     {"reason": "x", "odometer_km": 5, "confirmation": True})
    assert denied.status_code == 403
    corrected = api.agg(api.cmd(ADMIN, "vehicle.correct_snapshot", V1, busy["version"],
                                {"reason": "Исправлена запись одометра", "odometer_km": 6, "confirmation": True}))
    unchanged = api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))
    assert corrected["current_odometer_km"] == 6
    assert unchanged["status"] == "holding" and unchanged["version"] == checkout["version"]
    assert unchanged["inspection"]["version"] == checkout["inspection"]["version"]
    stale = api.cmd(ADMIN, "vehicle.correct_snapshot", V1, busy["version"],
                    {"reason": "Устаревшая версия", "odometer_km": 7, "confirmation": True})
    assert stale.status_code == 409 and stale.json()["error"]["code"] == "STALE_VERSION"


def test_five_mib_photo_accepted(api: Api) -> None:
    import io
    import os

    from PIL import Image

    side = int((5 * 1024 * 1024 / 3) ** 0.5)
    buf = io.BytesIO()
    Image.frombytes("RGB", (side, side), os.urandom(side * side * 3)).save(buf, format="PNG", compress_level=0)
    assert buf.tell() > 5 * 1024 * 1024 - 100_000
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    r = api.upload(DRIVER, insp["id"], 1, insp["version"], buf.getvalue())
    assert r.status_code == 200, r.text


def test_cross_expired_holds_no_deadlock(api: Api, app_and_store: tuple, monkeypatch: pytest.MonkeyPatch) -> None:
    """Ревью: A держит просроченный V1 и берёт V2, B держит просроченный V2 и берёт V1 — без 40P01."""
    from app.domain import executor
    from conftest import DRIVER2, V2

    monkeypatch.setattr(executor, "sweep", lambda factory: None)  # проверяем путь внутри команды
    for _ in range(5):
        api.agg(api.cmd(DRIVER, "checkout.create", V1, api.ok(api.get(f"/vehicles/{V1}", DRIVER))["version"]))
        api.agg(api.cmd(DRIVER2, "checkout.create", V2, api.ok(api.get(f"/vehicles/{V2}", DRIVER2))["version"]))
        with app_and_store[0].state.engine.begin() as conn:
            conn.execute(text("UPDATE checkout_attempts SET expires_at = now() - interval '1 second' "
                              "WHERE status = 'holding'"))
        v1 = api.ok(api.get(f"/vehicles/{V1}", ADMIN))["version"]
        v2 = api.ok(api.get(f"/vehicles/{V2}", ADMIN))["version"]
        barrier = threading.Barrier(2)

        def take(actor: str, vid: str, ver: int, gate: threading.Barrier = barrier) -> tuple[int, str | None]:
            gate.wait()
            r = api.cmd(actor, "checkout.create", vid, ver)
            return r.status_code, (r.json().get("error") or {}).get("code")

        with ThreadPoolExecutor(2) as pool:
            a = pool.submit(take, DRIVER, V2, v2)
            b = pool.submit(take, DRIVER2, V1, v1)
            results = [a.result(), b.result()]
        assert results == [(200, None), (200, None)], results  # без deadlock и ложных отказов
        with app_and_store[0].state.engine.connect() as conn:
            holds = conn.execute(text("SELECT count(*) FROM vehicle_assignments")).scalar_one()
        assert holds == sum(1 for status, _ in results if status == 200)
        with app_and_store[0].state.engine.begin() as conn:
            conn.execute(text("DELETE FROM vehicle_assignments"))
            conn.execute(text("UPDATE checkout_attempts SET status='cancelled' WHERE status='holding'"))
