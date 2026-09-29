"""Тестовая инфраструктура: настоящий PostgreSQL, миграции Alembic, синтетический seed.

TEST_DATABASE_URL — URL сервера PostgreSQL (по умолчанию локальный dev-контейнер);
тесты создают отдельную базу maxfleet_test и пересоздают её в начале сессии.
"""

from __future__ import annotations

import io
import itertools
import os
import tempfile
import uuid
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest
from alembic import command
from alembic.config import Config
from fastapi.testclient import TestClient
from PIL import Image
from sqlalchemy import create_engine, text

from app.config import Settings
from app.main import create_app
from app.models import Base
from app.seed import apply_synthetic
from app.storage.object_store import MemoryObjectStore

ROOT = Path(__file__).resolve().parent.parent
SERVER_URL = os.environ.get("TEST_DATABASE_URL", "postgresql+psycopg://postgres:devpass@127.0.0.1:55432/postgres")
TEST_DB = "maxfleet_test"
DATA_TOKEN = "test-data-token-0123456789"
WORKER_TOKEN = "test-worker-token-0123456789"

DRIVER = "8000000000000000001"
DRIVER2 = "8000000000000000002"
ADMIN = "8000000000000000003"
BLOCKED = "8000000000000000004"
UNKNOWN = "8000000000000000099"
V1 = "10000000-0000-4000-8000-000000000001"
V2 = "10000000-0000-4000-8000-000000000002"


def _db_url(name: str) -> str:
    base, _, _ = SERVER_URL.rpartition("/")
    return f"{base}/{name}"


@pytest.fixture(scope="session")
def database_url() -> str:
    admin = create_engine(SERVER_URL, isolation_level="AUTOCOMMIT")
    with admin.connect() as conn:
        conn.execute(text(f"DROP DATABASE IF EXISTS {TEST_DB} WITH (FORCE)"))
        conn.execute(text(f"CREATE DATABASE {TEST_DB}"))
    admin.dispose()
    url = _db_url(TEST_DB)
    with tempfile.NamedTemporaryFile("w", delete=False, suffix=".url") as handle:
        handle.write(url)
    os.environ["MIGRATION_DATABASE_URL_FILE"] = handle.name
    config = Config(str(ROOT / "alembic.ini"))
    config.set_main_option("script_location", str(ROOT / "migrations"))
    command.upgrade(config, "head")
    return url


@pytest.fixture(scope="session")
def settings(database_url: str) -> Settings:
    return Settings(database_url=database_url, data_api_token=DATA_TOKEN, worker_api_token=WORKER_TOKEN,
                    s3_endpoint=None, s3_bucket="test", s3_region="us-east-1", s3_access_key=None,
                    s3_secret_key=None, db_pool_size=20, db_max_overflow=20, db_lock_timeout_ms=5000,
                    db_statement_timeout_ms=10000)


@pytest.fixture(scope="session")
def app_and_store(settings: Settings) -> tuple[Any, MemoryObjectStore]:
    store = MemoryObjectStore()
    return create_app(settings, store), store


@pytest.fixture()
def store(app_and_store: tuple[Any, MemoryObjectStore]) -> MemoryObjectStore:
    return app_and_store[1]


@pytest.fixture(autouse=True)
def clean_db(app_and_store: tuple[Any, MemoryObjectStore]) -> Iterator[None]:
    app, store = app_and_store
    tables = ", ".join(t.name for t in Base.metadata.sorted_tables)
    with app.state.engine.begin() as conn:
        conn.execute(text(f"TRUNCATE {tables} RESTART IDENTITY CASCADE"))
    with app.state.factory.begin() as session:
        apply_synthetic(session, "demo-bot")
    store.objects.clear()
    store.fail_put = False
    store.fail_get = False
    yield


class Api:
    def __init__(self, client: TestClient) -> None:
        self.client = client
        self.counter = itertools.count(1)

    def headers(self, actor: str | None, *, worker: bool = False, key: str | None = None) -> dict[str, str]:
        headers = {"Authorization": f"Bearer {WORKER_TOKEN if worker else DATA_TOKEN}",
                   "X-Request-ID": str(uuid.uuid4()), "X-Contract-Version": "1.0"}
        if actor is not None:
            headers["X-Actor-Max-ID"] = actor
        if key is not None:
            headers["Idempotency-Key"] = key
        return headers

    def get(self, path: str, actor: str | None = DRIVER, **params: Any) -> Any:
        return self.client.get("/internal/v1" + path, headers=self.headers(actor), params=params)

    def cmd(self, actor: str, operation: str, target: str | None, version: int | None,
            payload: dict[str, Any] | None = None, *, key: str | None = None) -> Any:
        key = key or f"key-{next(self.counter):06d}-{uuid.uuid4().hex[:8]}"
        body = {"operation": operation, "target_id": target, "expected_version": version,
                "payload": payload or {}}
        return self.client.post("/internal/v1/commands", headers=self.headers(actor, key=key), json=body)

    def ok(self, response: Any) -> Any:
        assert response.status_code == 200, response.text
        return response.json()["data"]

    def agg(self, response: Any) -> Any:
        return self.ok(response)["aggregate"]

    def upload(self, actor: str, inspection_id: str, slot: int, version: int, image: bytes, *,
               event: str | None = None, key: str | None = None, mime: str = "image/png") -> Any:
        key = key or f"photo-{next(self.counter):06d}-{uuid.uuid4().hex[:8]}"
        event = event or f"message:{uuid.uuid4().hex}:message_created"
        return self.client.post(
            f"/internal/v1/inspections/{inspection_id}/photos/{slot}", headers=self.headers(actor, key=key),
            data={"expected_version": str(version), "source_event_key": event},
            files={"image": ("photo.png", image, mime)})

    def worker(self, path: str, body: dict[str, Any], *, key: str | None = None) -> Any:
        key = key or f"worker-{next(self.counter):06d}-{uuid.uuid4().hex[:8]}"
        return self.client.post("/internal/v1" + path, headers=self.headers(None, worker=True, key=key),
                                json=body)


@pytest.fixture()
def api(app_and_store: tuple[Any, MemoryObjectStore]) -> Api:
    return Api(TestClient(app_and_store[0]))


_colors = itertools.count(1)


def png(color: int | None = None, size: tuple[int, int] = (8, 8)) -> bytes:
    value = color if color is not None else next(_colors)
    img = Image.new("RGB", size, ((value * 37) % 256, (value * 91) % 256, (value * 53) % 256))
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


# ------------------------------------------------------------------ сценарии

def take_until_inspection(api: Api, actor: str = DRIVER, vehicle: str = V1) -> dict[str, Any]:
    """Создать hold, решить пример и принять правила; вернуть checkout."""
    v = api.ok(api.get(f"/vehicles/{vehicle}", actor))
    checkout = api.agg(api.cmd(actor, "checkout.create", vehicle, v["version"]))
    v = api.ok(api.get(f"/vehicles/{vehicle}", actor))
    ch = api.agg(api.cmd(actor, "challenge.create", checkout["id"], checkout["version"], {
        "purpose": "take",
        "intent_payload": {"operation": "checkout.create", "target_id": vehicle,
                           "expected_version": v["version"] - 1}}))
    solve(api, actor, ch)
    checkout = api.ok(api.get(f"/checkouts/{checkout['id']}", actor))
    rules = api.ok(api.get("/rules/current", actor))
    return api.agg(api.cmd(actor, "checkout.accept_rules", checkout["id"], checkout["version"],
                           {"rules_version_id": rules["id"]}))


def solve(api: Api, actor: str, challenge: dict[str, Any]) -> dict[str, Any]:
    a, b = (int(x) for x in challenge["question"].split(" = ")[0].split(" + "))
    index = challenge["options"].index(a + b)
    return api.ok(api.cmd(actor, "challenge.answer", challenge["id"], challenge["version"],
                          {"selected_option": index}))


def upload_all(api: Api, actor: str, inspection: dict[str, Any], slots: range = range(1, 9)) -> dict[str, Any]:
    version = inspection["version"]
    current = inspection
    for slot in slots:
        current = api.ok(api.upload(actor, inspection["id"], slot, version, png()))["inspection"]
        version = current["version"]
    return current


def start_trip(api: Api, actor: str = DRIVER, vehicle: str = V1, odometer: int | None = None) -> dict[str, Any]:
    checkout = take_until_inspection(api, actor, vehicle)
    insp = checkout["inspection"]
    v = api.ok(api.get(f"/vehicles/{vehicle}", actor))
    insp = api.agg(api.cmd(actor, "inspection.update", insp["id"], insp["version"],
                           {"fuel_level": 50, "odometer_km": odometer or v["current_odometer_km"] + 10}))
    insp = upload_all(api, actor, insp)
    insp = api.agg(api.cmd(actor, "inspection.confirm_photos", insp["id"], insp["version"]))
    checkout = api.ok(api.get(f"/checkouts/{checkout['id']}", actor))
    checkout = api.agg(api.cmd(actor, "checkout.set_no_new_issues", checkout["id"], checkout["version"],
                               {"value": True}))
    return api.agg(api.cmd(actor, "checkout.start", checkout["id"], checkout["version"], {"attestation": True}))


def begin_return(api: Api, actor: str, trip: dict[str, Any]) -> dict[str, Any]:
    ret = api.agg(api.cmd(actor, "trip.begin_return", trip["id"], trip["version"]))
    trip = api.ok(api.get(f"/trips/{trip['id']}", actor))
    ch = api.agg(api.cmd(actor, "challenge.create", ret["id"], ret["version"], {
        "purpose": "return",
        "intent_payload": {"operation": "trip.begin_return", "target_id": trip["id"],
                           "expected_version": trip["version"] - 1}}))
    solve(api, actor, ch)
    return api.ok(api.get(f"/returns/{ret['id']}", actor))


def fill_return(api: Api, actor: str, ret: dict[str, Any], **answers: Any) -> dict[str, Any]:
    insp = ret["inspection"]
    trip = api.ok(api.get(f"/trips/{ret['trip_id']}", actor))
    data = {"fuel_level": 25, "odometer_km": trip["before_inspection"]["odometer_km"] + 42, "new_damage": False,
            "cabin_clean": True, "parking_allowed": True, "keys_returned": True, "car_locked": True}
    data.update(answers)
    insp = api.agg(api.cmd(actor, "inspection.update", insp["id"], insp["version"], data))
    insp = upload_all(api, actor, insp)
    api.agg(api.cmd(actor, "inspection.confirm_photos", insp["id"], insp["version"]))
    ret = api.ok(api.get(f"/returns/{ret['id']}", actor))
    ret = api.agg(api.cmd(actor, "return.set_location", ret["id"], ret["version"], {
        "latitude": 55.751, "longitude": 37.62, "source": "manual_map", "landmark": "У входа", "confirmed": True}))
    return ret
