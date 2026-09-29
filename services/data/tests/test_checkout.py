"""Оформление: гонки, TTL, математическая проверка, идемпотентность."""

from __future__ import annotations

import threading
from concurrent.futures import ThreadPoolExecutor

from sqlalchemy import text

from conftest import (
    ADMIN,
    DRIVER,
    DRIVER2,
    V1,
    V2,
    Api,
    solve,
    take_until_inspection,
)

EMPLOYEES_FOR_RACE = [str(9000000000000000000 + i) for i in range(20)]


def _grant_many(api: Api, app_engine: object) -> None:
    import uuid

    with app_engine.begin() as conn:  # type: ignore[attr-defined]
        for max_id in EMPLOYEES_FOR_RACE:
            conn.execute(text("INSERT INTO employees (id, max_user_id, display_name, role, can_start_trip) "
                              "VALUES (:id, :mid, 'Гонщик', 'employee', true)"),
                         {"id": uuid.uuid4(), "mid": int(max_id)})


def test_race_twenty_employees_one_vehicle(api: Api, app_and_store: tuple) -> None:
    """AC-03: 20 одновременных запросов одной машины → ровно один assignment."""
    _grant_many(api, app_and_store[0].state.engine)
    barrier = threading.Barrier(len(EMPLOYEES_FOR_RACE))

    def take(max_id: str) -> tuple[int, str | None]:
        barrier.wait()
        r = api.cmd(max_id, "checkout.create", V1, 1)
        return r.status_code, (r.json().get("error") or {}).get("code")

    with ThreadPoolExecutor(len(EMPLOYEES_FOR_RACE)) as pool:
        results = list(pool.map(take, EMPLOYEES_FOR_RACE))
    winners = [r for r in results if r[0] == 200]
    losers = {r[1] for r in results if r[0] != 200}
    assert len(winners) == 1, results
    assert losers <= {"VEHICLE_UNAVAILABLE", "STALE_VERSION"}, losers
    with app_and_store[0].state.engine.connect() as conn:
        assert conn.execute(text("SELECT count(*) FROM vehicle_assignments")).scalar_one() == 1


def test_race_one_employee_many_vehicles(api: Api, app_and_store: tuple) -> None:
    vehicles = [f"10000000-0000-4000-8000-0000000000{i:02d}" for i in range(1, 11)]
    barrier = threading.Barrier(len(vehicles))

    def take(vehicle: str) -> int:
        barrier.wait()
        return api.cmd(DRIVER, "checkout.create", vehicle, 1).status_code

    with ThreadPoolExecutor(len(vehicles)) as pool:
        codes = list(pool.map(take, vehicles))
    assert codes.count(200) == 1, codes
    with app_and_store[0].state.engine.connect() as conn:
        assert conn.execute(text("SELECT count(*) FROM vehicle_assignments")).scalar_one() == 1


def test_duplicate_command_returns_same_result(api: Api) -> None:
    """AC-20: повтор после потери ответа — тот же результат, без второго hold."""
    first = api.cmd(DRIVER, "checkout.create", V1, 1, key="same-key-123")
    again = api.cmd(DRIVER, "checkout.create", V1, 1, key="same-key-123")
    assert first.status_code == again.status_code == 200
    assert first.json()["data"] == again.json()["data"]
    conflict = api.cmd(DRIVER, "checkout.create", V2, 1, key="same-key-123")
    assert conflict.status_code == 409 and conflict.json()["error"]["code"] == "IDEMPOTENCY_CONFLICT"
    other_actor = api.cmd(DRIVER2, "checkout.create", V2, 1, key="same-key-123")
    assert other_actor.status_code == 200  # область ключа — actor
    stored = api.client.get("/internal/v1/commands/same-key-123", headers=api.headers(DRIVER),
                            params={"operation": "checkout.create"})
    assert stored.status_code == 200 and stored.json()["data"] == first.json()["data"]
    foreign = api.client.get("/internal/v1/commands/same-key-123", headers=api.headers(ADMIN),
                             params={"operation": "checkout.create"})
    assert foreign.status_code == 404


def test_hold_expiry_releases_vehicle(api: Api, app_and_store: tuple) -> None:
    """AC-11: по истечении 15 минут hold освобождается; старой кнопкой не начать."""
    checkout = take_until_inspection(api, DRIVER, V1)
    assert checkout["step"] == "inspection"
    with app_and_store[0].state.engine.begin() as conn:
        conn.execute(text("UPDATE checkout_attempts SET expires_at = now() - interval '1 second'"))
        conn.execute(text("UPDATE vehicle_assignments SET hold_expires_at = now() - interval '1 second'"))
    start = api.cmd(DRIVER, "checkout.start", checkout["id"], checkout["version"], {"attestation": True})
    assert start.status_code == 409 and start.json()["error"]["code"] == "HOLD_EXPIRED"
    assert api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))["status"] == "expired"
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER2))
    assert vehicle["status"] == "available"
    taken = api.cmd(DRIVER2, "checkout.create", V1, vehicle["version"])
    assert taken.status_code == 200


def test_challenge_rules(api: Api) -> None:
    checkout = api.agg(api.cmd(DRIVER, "checkout.create", V1, 1))
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    intent = {"operation": "checkout.create", "target_id": V1, "expected_version": vehicle["version"] - 1}
    ch = api.agg(api.cmd(DRIVER, "challenge.create", checkout["id"], checkout["version"],
                         {"purpose": "take", "intent_payload": intent}))
    assert len(set(ch["options"])) == 4 and ch["attempts_remaining"] == 3
    assert "correct_answer" not in str(ch)
    a, b = (int(x) for x in ch["question"].split(" = ")[0].split(" + "))
    wrong = next(i for i, o in enumerate(ch["options"]) if o != a + b)
    for expected_left in (2, 1, 0):
        res = api.ok(api.cmd(DRIVER, "challenge.answer", ch["id"], ch["version"], {"selected_option": wrong}))
        assert res["correct"] is False and res["attempts_remaining"] == expected_left
        ch = res["aggregate"]
    blocked = api.cmd(DRIVER, "challenge.answer", ch["id"], ch["version"], {"selected_option": 0})
    assert blocked.status_code == 409  # после трёх ошибок — новый пример
    state = api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))
    assert state["intent_confirmed_at"] is None and state["step"] == "math"  # AC-05
    ch2 = api.agg(api.cmd(DRIVER, "challenge.create", checkout["id"], state["version"],
                          {"purpose": "take", "intent_payload": intent}))
    ok = solve(api, DRIVER, ch2)
    assert ok["correct"] is True
    replay = api.cmd(DRIVER, "challenge.answer", ch2["id"], ok["aggregate"]["version"], {"selected_option": 0})
    assert replay.status_code == 409  # решённый пример нельзя использовать повторно
    assert api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))["step"] == "rules"
    current = api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))
    rules_required = api.cmd(DRIVER, "checkout.start", current["id"], current["version"], {"attestation": True})
    assert rules_required.status_code == 422 and rules_required.json()["error"]["code"] == "RULES_REQUIRED"


def test_expired_math_challenge_is_422(api: Api, app_and_store: tuple) -> None:
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    checkout = api.agg(api.cmd(DRIVER, "checkout.create", V1, vehicle["version"]))
    current_vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    challenge = api.agg(api.cmd(DRIVER, "challenge.create", checkout["id"], checkout["version"], {
        "purpose": "take",
        "intent_payload": {"operation": "checkout.create", "target_id": V1,
                           "expected_version": current_vehicle["version"] - 1}}))
    app, _store = app_and_store
    with app.state.engine.begin() as connection:
        connection.execute(text("UPDATE challenges SET expires_at = now() - interval '1 second' WHERE id = :id"),
                           {"id": challenge["id"]})
    expired = api.cmd(DRIVER, "challenge.answer", challenge["id"], challenge["version"], {"selected_option": 0})
    assert expired.status_code == 422 and expired.json()["error"]["code"] == "CHALLENGE_EXPIRED"


def test_start_requirements(api: Api) -> None:
    checkout = take_until_inspection(api, DRIVER, V1)
    early = api.cmd(DRIVER, "checkout.start", checkout["id"], checkout["version"], {"attestation": True})
    assert early.status_code == 422 and early.json()["error"]["code"] == "PHOTO_SET_INCOMPLETE"
    assert early.json()["error"]["details"]["missing_slots"] == [1, 2, 3, 4, 5, 6, 7, 8]
    insp = checkout["inspection"]
    rollback = api.cmd(DRIVER, "inspection.update", insp["id"], insp["version"], {"odometer_km": 5})
    assert rollback.status_code == 422 and rollback.json()["error"]["code"] == "ODOMETER_ROLLBACK"  # AC-26
    bad_fuel = api.cmd(DRIVER, "inspection.update", insp["id"], insp["version"], {"fuel_level": 37})
    assert bad_fuel.status_code == 400
    wrong_field = api.cmd(DRIVER, "inspection.update", insp["id"], insp["version"], {"keys_returned": True})
    assert wrong_field.status_code == 409  # вопросы возврата недоступны до выезда


def test_schema_strictness(api: Api) -> None:
    base = {"operation": "checkout.create", "target_id": V1, "expected_version": 1, "payload": {}}
    for body in (
        {**base, "target_id": "not-a-uuid"},
        {k: v for k, v in base.items() if k != "payload"},
        {**base, "extra": 1},
        {**base, "payload": {"unexpected": True}},
        {**base, "expected_version": "1"},
        {**base, "expected_version": True},
    ):
        r = api.client.post("/internal/v1/commands", headers=api.headers(DRIVER, key="strict-key-1"), json=body)
        assert r.status_code == 400 and r.json()["error"]["code"] == "INVALID_REQUEST", body
    no_key = api.client.post("/internal/v1/commands", headers=api.headers(DRIVER), json=base)
    assert no_key.status_code == 400
