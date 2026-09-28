"""Smoke-проверка работающего data-api (реальные PostgreSQL + S3), без вывода секретов.

    DATA_API_URL=http://127.0.0.1:18000 MAX_FLEET_SECRETS_DIR=... python scripts/smoke.py

Нужен синтетический seed (SEED_SYNTHETIC=1). Проходит взятие → поездку → возврат с 16 фото.
"""

from __future__ import annotations

import io
import json
import os
import sys
import urllib.error
import urllib.request
import uuid
from pathlib import Path

BASE = os.environ.get("DATA_API_URL", "http://127.0.0.1:18000")
SECRETS = Path(os.environ["MAX_FLEET_SECRETS_DIR"])
TOKEN = (SECRETS / "data_api_token").read_text().strip()
WORKER = (SECRETS / "worker_api_token").read_text().strip()
DRIVER, ADMIN = "8000000000000000001", "8000000000000000003"


def call(method: str, path: str, actor: str | None, body: bytes | None = None, *, ctype: str | None = None,
         key: str | None = None, worker: bool = False) -> tuple[int, dict]:
    headers = {"Authorization": f"Bearer {WORKER if worker else TOKEN}", "X-Request-ID": str(uuid.uuid4()),
               "X-Contract-Version": "1.0"}
    if actor:
        headers["X-Actor-Max-ID"] = actor
    if key:
        headers["Idempotency-Key"] = key
    if ctype:
        headers["Content-Type"] = ctype
    # BASE — адрес data-api из окружения оператора (http), не пользовательский ввод.
    req = urllib.request.Request(BASE + path, data=body, headers=headers, method=method)  # noqa: S310
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as err:
        return err.code, json.loads(err.read())


def get(path: str, actor: str = DRIVER) -> dict:
    status, body = call("GET", "/internal/v1" + path, actor)
    assert status == 200, (path, status, body)
    return body["data"]


def cmd(op: str, target: str | None, version: int | None, payload: dict | None = None, actor: str = DRIVER) -> dict:
    body = json.dumps({"operation": op, "target_id": target, "expected_version": version,
                       "payload": payload or {}}).encode()
    status, data = call("POST", "/internal/v1/commands", actor, body, ctype="application/json",
                        key=f"smoke-{uuid.uuid4().hex}")
    assert status == 200, (op, status, data)
    return data["data"]


def png(seed: int) -> bytes:
    # Минимальный валидный PNG без Pillow: 1x1 пиксель уникального цвета.
    import struct
    import zlib

    raw = b"\x00" + bytes([seed % 256, (seed * 7) % 256, (seed * 13) % 256])

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


def upload(inspection: dict, seed: int) -> dict:
    version = inspection["version"]
    for slot in range(1, 9):
        boundary = uuid.uuid4().hex
        buf = io.BytesIO()
        for name, value in (("expected_version", str(version)),
                            ("source_event_key", f"message:{uuid.uuid4().hex}:message_created")):
            buf.write(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode())
        buf.write(f"--{boundary}\r\nContent-Disposition: form-data; name=\"image\"; filename=\"p.png\"\r\n"
                  "Content-Type: image/png\r\n\r\n".encode())
        buf.write(png(seed * 10 + slot) + f"\r\n--{boundary}--\r\n".encode())
        status, data = call("POST", f"/internal/v1/inspections/{inspection['id']}/photos/{slot}", DRIVER,
                            buf.getvalue(), ctype=f"multipart/form-data; boundary={boundary}",
                            key=f"smoke-photo-{uuid.uuid4().hex}")
        assert status == 200, (slot, status, data)
        inspection = data["data"]["inspection"]
        version = inspection["version"]
    return inspection


def solve(ch: dict, actor: str = DRIVER) -> None:
    a, b = (int(x) for x in ch["question"].split(" = ")[0].split(" + "))
    res = cmd("challenge.answer", ch["id"], ch["version"], {"selected_option": ch["options"].index(a + b)}, actor)
    assert res["correct"] is True


def main() -> int:
    status, ready = call("GET", "/health/ready", None)
    assert status == 200, ready
    vehicle = get("/vehicles?available=true")["items"][0]
    checkout = cmd("checkout.create", vehicle["id"], vehicle["version"])["aggregate"]
    vehicle = get(f"/vehicles/{vehicle['id']}")
    solve(cmd("challenge.create", checkout["id"], checkout["version"], {"purpose": "take", "intent_payload": {
        "operation": "checkout.create", "target_id": vehicle["id"], "expected_version": vehicle["version"] - 1}})
        ["aggregate"])
    checkout = get(f"/checkouts/{checkout['id']}")
    rules = get("/rules/current")
    checkout = cmd("checkout.accept_rules", checkout["id"], checkout["version"],
                   {"rules_version_id": rules["id"]})["aggregate"]
    insp = cmd("inspection.update", checkout["inspection"]["id"], checkout["inspection"]["version"],
               {"fuel_level": 75, "odometer_km": vehicle["current_odometer_km"] + 1})["aggregate"]
    insp = upload(insp, 1)
    cmd("inspection.confirm_photos", insp["id"], insp["version"])
    checkout = get(f"/checkouts/{checkout['id']}")
    checkout = cmd("checkout.set_no_new_issues", checkout["id"], checkout["version"], {"value": True})["aggregate"]
    trip = cmd("checkout.start", checkout["id"], checkout["version"], {"attestation": True})["aggregate"]
    print("trip started:", trip["status"])
    ret = cmd("trip.begin_return", trip["id"], trip["version"])["aggregate"]
    trip = get(f"/trips/{trip['id']}")
    solve(cmd("challenge.create", ret["id"], ret["version"], {"purpose": "return", "intent_payload": {
        "operation": "trip.begin_return", "target_id": trip["id"], "expected_version": trip["version"] - 1}})
        ["aggregate"])
    ret = get(f"/returns/{ret['id']}")
    insp = cmd("inspection.update", ret["inspection"]["id"], ret["inspection"]["version"], {
        "fuel_level": 50, "odometer_km": vehicle["current_odometer_km"] + 30, "new_damage": False,
        "cabin_clean": True, "parking_allowed": True, "keys_returned": True, "car_locked": True})["aggregate"]
    insp = upload(insp, 2)
    cmd("inspection.confirm_photos", insp["id"], insp["version"])
    ret = get(f"/returns/{ret['id']}")
    ret = cmd("return.set_location", ret["id"], ret["version"], {
        "latitude": 55.7512, "longitude": 37.6184, "source": "manual_map", "confirmed": True})["aggregate"]
    done = cmd("return.complete", ret["id"], ret["version"], {"attestation": True})["aggregate"]
    print("return:", done["status"])
    trip = get(f"/trips/{trip['id']}")
    asset_status, _ = call("GET", f"/internal/v1/vehicles/{vehicle['id']}/previous-inspection", DRIVER)
    status, claim = call("POST", "/internal/v1/notifications/claim", None,
                         json.dumps({"worker_id": "smoke", "max_items": 10}).encode(), ctype="application/json",
                         key=f"smoke-{uuid.uuid4().hex}", worker=True)
    assert status == 200
    print("trip:", trip["status"], "| previous-inspection:", asset_status,
          "| notifications:", [i["event"]["type"] for i in claim["data"]["items"]])
    return 0


if __name__ == "__main__":
    sys.exit(main())
