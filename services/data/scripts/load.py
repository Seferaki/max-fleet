"""Нагрузочная проверка DE-08 против работающего data-api (без вывода секретов).

    DATA_API_URL=http://127.0.0.1:18000 MAX_FLEET_SECRETS_DIR=... python scripts/load.py \
        --users 50 --rps 20 --seconds 600
    ... python scripts/load.py --uploads 10 --upload-mib 5

Смесь: ~70% чтений (me, state, список и карточка машины, мои поездки) и ~30% команд
(conversation.save с CAS, checkout.create/cancel с конкуренцией за 10 машин).
Отчёт: p50/p95/p99, доля 5xx, коды 409 (ожидаемые конфликты) отдельно.
"""

from __future__ import annotations

import argparse
import io
import json
import os
import random
import statistics
import threading
import time
import uuid
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import httpx

BASE = os.environ.get("DATA_API_URL", "http://127.0.0.1:18000")
SECRETS = Path(os.environ["MAX_FLEET_SECRETS_DIR"])
TOKEN = (SECRETS / "data_api_token").read_text().strip()
ADMIN = "8000000000000000003"
LOAD_BASE = 7_700_000_000_000_000_000


def headers(actor: str, key: str | None = None) -> dict[str, str]:
    h = {"Authorization": f"Bearer {TOKEN}", "X-Request-ID": str(uuid.uuid4()),
         "X-Contract-Version": "1.0", "X-Actor-Max-ID": actor}
    if key:
        h["Idempotency-Key"] = key
    return h


def command(client: httpx.Client, actor: str, op: str, target: str | None, version: int | None,
            payload: dict) -> httpx.Response:
    return client.post("/internal/v1/commands", headers=headers(actor, f"load-{uuid.uuid4().hex}"),
                       json={"operation": op, "target_id": target, "expected_version": version, "payload": payload})


def ensure_users(client: httpx.Client, count: int) -> list[str]:
    users = [str(LOAD_BASE + i) for i in range(count)]
    for max_id in users:
        me = client.get("/internal/v1/me", headers=headers(max_id)).json()["data"]
        if me["allowed"]:
            continue
        intent = {"operation": "employee.grant", "target_id": None, "expected_version": None,
                  "max_user_id": max_id, "display_name": f"Нагрузка {max_id[-3:]}"}
        ch = command(client, ADMIN, "challenge.create", None, None,
                     {"purpose": "employee_grant", "intent_payload": intent}).json()["data"]["aggregate"]
        a, b = (int(x) for x in ch["question"].split(" = ")[0].split(" + "))
        command(client, ADMIN, "challenge.answer", ch["id"], ch["version"],
                {"selected_option": ch["options"].index(a + b)})
        r = command(client, ADMIN, "employee.grant", None, None, {
            "max_user_id": max_id, "display_name": intent["display_name"], "challenge_id": ch["id"]})
        assert r.status_code == 200, r.text
    return users


def one_request(client: httpx.Client, actor: str, vehicles: list[str], state: dict) -> tuple[str, int, float]:
    roll = random.random()
    started = time.perf_counter()
    if roll < 0.15:
        name, r = "me", client.get("/internal/v1/me", headers=headers(actor))
    elif roll < 0.30:
        name, r = "state", client.get("/internal/v1/state", headers=headers(actor))
    elif roll < 0.50:
        name, r = "vehicles", client.get("/internal/v1/vehicles", params={"available": "true"}, headers=headers(actor))
    elif roll < 0.62:
        name, r = "vehicle", client.get(f"/internal/v1/vehicles/{random.choice(vehicles)}", headers=headers(actor))
    elif roll < 0.70:
        name, r = "trips", client.get("/internal/v1/trips", params={"scope": "mine"}, headers=headers(actor))
    elif roll < 0.85:
        emp_id = state.setdefault(actor, {}).get("employee_id")
        if emp_id is None:
            emp_id = client.get("/internal/v1/me", headers=headers(actor)).json()["data"]["employee"]["id"]
            state[actor]["employee_id"] = emp_id
        version = state[actor].get("conv", 1)
        name = "conversation.save"
        r = command(client, actor, name, emp_id, version, {
            "flow": "menu", "step": random.choice(["main", "vehicles", "trips"]), "context": {}})
        if r.status_code == 200:
            state[actor]["conv"] = r.json()["data"]["aggregate"]["version"]
        elif r.status_code == 409:
            state[actor]["conv"] = r.json()["error"].get("details", {}).get("current_version", version)
    else:
        hold = state.setdefault(actor, {}).get("hold")
        if hold:
            name = "checkout.cancel"
            r = command(client, actor, name, hold["id"], hold["version"], {})
            state[actor]["hold"] = None
        else:
            vid = random.choice(vehicles)
            card = client.get(f"/internal/v1/vehicles/{vid}", headers=headers(actor)).json()["data"]
            name = "checkout.create"
            r = command(client, actor, name, vid, card["version"], {})
            if r.status_code == 200:
                agg = r.json()["data"]["aggregate"]
                state[actor]["hold"] = {"id": agg["id"], "version": agg["version"]}
    return name, r.status_code, (time.perf_counter() - started) * 1000


def run_load(users: list[str], rps: float, seconds: int, workers: int) -> dict:
    vehicles = [f"10000000-0000-4000-8000-0000000000{i:02d}" for i in range(1, 11)]
    results: list[tuple[str, int, float]] = []
    lock = threading.Lock()
    state: dict = {}
    locks = {u: threading.Lock() for u in users}
    deadline = time.monotonic() + seconds
    interval = 1.0 / rps
    clients = threading.local()

    def task(actor: str) -> None:
        if not hasattr(clients, "c"):
            clients.c = httpx.Client(base_url=BASE, timeout=10)
        with locks[actor]:  # один пользователь — последовательные действия, как в чате
            try:
                res = one_request(clients.c, actor, vehicles, state)
            except httpx.HTTPError:
                res = ("transport", 599, 0.0)
        with lock:
            results.append(res)

    with ThreadPoolExecutor(workers) as pool:
        next_at = time.monotonic()
        i = 0
        while time.monotonic() < deadline:
            pool.submit(task, users[i % len(users)])
            i += 1
            next_at += interval
            delay = next_at - time.monotonic()
            if delay > 0:
                time.sleep(delay)
    lat = sorted(r[2] for r in results)

    def pct(p: float) -> float:
        return round(lat[min(len(lat) - 1, int(p * len(lat)))], 1) if lat else 0.0

    codes = Counter(r[1] for r in results)
    return {
        "requests": len(results), "duration_s": seconds, "achieved_rps": round(len(results) / seconds, 2),
        "p50_ms": pct(0.50), "p95_ms": pct(0.95), "p99_ms": pct(0.99),
        "mean_ms": round(statistics.fmean(lat), 1) if lat else 0,
        "codes": dict(sorted(codes.items())),
        "error_5xx_rate": round(sum(v for k, v in codes.items() if k >= 500) / max(1, len(results)), 5),
        "by_op": {op: dict(Counter(c for o, c, _ in results if o == op)) for op in sorted({r[0] for r in results})},
    }


def run_uploads(count: int, mib: int) -> dict:
    """Параллельные загрузки count фото по ~mib MiB в разные осмотры (нужен свободный hold на машину)."""
    from PIL import Image

    client = httpx.Client(base_url=BASE, timeout=60)
    users = ensure_users(client, count)
    vehicles = [f"10000000-0000-4000-8000-0000000000{i:02d}" for i in range(1, 11)]
    targets = []
    for actor, vid in zip(users, vehicles, strict=False):
        card = client.get(f"/internal/v1/vehicles/{vid}", headers=headers(actor)).json()["data"]
        r = command(client, actor, "checkout.create", vid, card["version"], {})
        if r.status_code != 200:
            continue
        chk = r.json()["data"]["aggregate"]
        card = client.get(f"/internal/v1/vehicles/{vid}", headers=headers(actor)).json()["data"]
        intent = {"operation": "checkout.create", "target_id": vid, "expected_version": card["version"] - 1}
        ch = command(client, actor, "challenge.create", chk["id"], chk["version"],
                     {"purpose": "take", "intent_payload": intent}).json()["data"]["aggregate"]
        a, b = (int(x) for x in ch["question"].split(" = ")[0].split(" + "))
        command(client, actor, "challenge.answer", ch["id"], ch["version"],
                {"selected_option": ch["options"].index(a + b)})
        chk = client.get(f"/internal/v1/checkouts/{chk['id']}", headers=headers(actor)).json()["data"]
        rules = client.get("/internal/v1/rules/current", headers=headers(actor)).json()["data"]
        chk = command(client, actor, "checkout.accept_rules", chk["id"], chk["version"],
                      {"rules_version_id": rules["id"]}).json()["data"]["aggregate"]
        targets.append((actor, chk["inspection"]))
    side = int((mib * 1024 * 1024 / 3) ** 0.5)
    payloads = []
    for _ in range(len(targets)):
        img = Image.frombytes("RGB", (side, side), os.urandom(side * side * 3))
        buf = io.BytesIO()
        img.save(buf, format="PNG", compress_level=0)
        payloads.append(buf.getvalue())

    def upload(idx: int) -> tuple[int, float, int]:
        actor, insp = targets[idx]
        started = time.perf_counter()
        form = {"expected_version": str(insp["version"]),
                "source_event_key": f"message:{uuid.uuid4().hex}:message_created"}
        r = httpx.post(f"{BASE}/internal/v1/inspections/{insp['id']}/photos/1",
                       headers=headers(actor, f"load-up-{uuid.uuid4().hex}"), data=form,
                       files={"image": ("p.png", payloads[idx], "image/png")}, timeout=60)
        return r.status_code, (time.perf_counter() - started) * 1000, len(payloads[idx])

    with ThreadPoolExecutor(len(targets)) as pool:
        res = list(pool.map(upload, range(len(targets))))
    for actor, _insp in targets:  # освободить машины
        chk_state = client.get("/internal/v1/state", headers=headers(actor)).json()["data"]["checkout"]
        if chk_state:
            command(client, actor, "checkout.cancel", chk_state["id"], chk_state["version"], {})
    return {"uploads": len(res), "codes": dict(Counter(r[0] for r in res)),
            "size_mib": [round(r[2] / 1048576, 2) for r in res],
            "max_ms": round(max(r[1] for r in res), 1) if res else 0,
            "mean_ms": round(statistics.fmean(r[1] for r in res), 1) if res else 0}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--users", type=int, default=50)
    parser.add_argument("--rps", type=float, default=20)
    parser.add_argument("--seconds", type=int, default=600)
    parser.add_argument("--workers", type=int, default=50)
    parser.add_argument("--uploads", type=int, default=0)
    parser.add_argument("--upload-mib", type=int, default=5)
    args = parser.parse_args()
    if args.uploads:
        print(json.dumps(run_uploads(args.uploads, args.upload_mib), ensure_ascii=False, indent=1))
        return
    with httpx.Client(base_url=BASE, timeout=30) as client:
        users = ensure_users(client, args.users)
    print(json.dumps(run_load(users, args.rps, args.seconds, args.workers), ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
