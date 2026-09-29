"""Идемпотентный синтетический seed (только dev/test) и приватный bootstrap администратора.

    python -m app.seed synthetic         # 10 демо-машин, 4 демо-сотрудника, правила, интеграция
    python -m app.seed bootstrap-admin   # администратор из BOOTSTRAP_ADMIN_MAX_ID_FILE

Идентификаторы совпадают с Go data-mock, чтобы сценарии INT были воспроизводимы.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
import uuid
from datetime import UTC, datetime
from decimal import Decimal
from pathlib import Path

from sqlalchemy import select
from sqlalchemy.orm import Session

from app import models as m
from app.config import ConfigError, Settings, _read_secret
from app.db import make_engine, make_session_factory, unit_of_work

SEED_FILE = Path(__file__).resolve().parent.parent / "fixtures" / "synthetic-seed.json"
STAMP = datetime(2026, 9, 27, 9, 0, 0, tzinfo=UTC)
RULES_ID = uuid.UUID("90000000-0000-4000-8000-000000000001")


def normalize_plate(plate: str) -> str:
    return re.sub(r"[\s\-]", "", plate).upper()


def employee_id(index: int) -> uuid.UUID:
    return uuid.UUID(f"80000000-0000-4000-8000-{index:012d}")


def parking_id(index: int) -> uuid.UUID:
    return uuid.UUID(f"91000000-0000-4000-8000-{index:012d}")


def apply_synthetic(session: Session, integration_key: str) -> dict[str, int]:
    seed = json.loads(SEED_FILE.read_text(encoding="utf-8"))
    if seed.get("demo_only") is not True or len(seed["vehicles"]) != 10:
        raise SystemExit("seed: ожидается demo_only=true и 10 машин")
    created = {"employees": 0, "vehicles": 0, "rules": 0, "integrations": 0}
    if session.get(m.RulesVersion, RULES_ID) is None:
        body = seed["rules"]
        session.add(m.RulesVersion(id=RULES_ID, version_label="demo-v1", body=body,
                                   body_sha256=hashlib.sha256(body.encode()).hexdigest(),
                                   effective_at=STAMP, is_current=True, created_at=STAMP))
        created["rules"] += 1
    for index, item in enumerate(seed["employees"], start=1):
        max_id = int(item["max_user_id"])
        if session.scalar(select(m.Employee.id).where(m.Employee.max_user_id == max_id)) is not None:
            continue
        can_start = bool(item["can_start_trip"])
        session.add(m.Employee(id=employee_id(index), max_user_id=max_id, display_name=item["display_name"],
                               role=item["role"], can_start_trip=can_start,
                               access_block_reason=None if can_start else "Демо: запрет новых поездок",
                               version=1, created_at=STAMP, updated_at=STAMP))
        created["employees"] += 1
    session.flush()
    for index, item in enumerate(seed["vehicles"], start=1):
        vid = uuid.UUID(item["id"])
        if session.get(m.Vehicle, vid) is not None:
            continue
        session.add(m.Vehicle(id=vid, plate=item["plate"], plate_normalized=normalize_plate(item["plate"]),
                              make="Демо", model=item["model"], description="Синтетический автомобиль",
                              key_instructions=item["key_instructions"], current_fuel=item["fuel_level"],
                              fuel_confirmed_at=STAMP, current_odometer_km=item["odometer_km"],
                              odometer_confirmed_at=STAMP, version=1, created_at=STAMP, updated_at=STAMP))
        session.flush()
        point = m.ParkingLocation(id=parking_id(index), vehicle_id=vid,
                                  latitude=Decimal(str(item["parking"]["latitude"])),
                                  longitude=Decimal(str(item["parking"]["longitude"])),
                                  source="seed", confirmed_at=STAMP, created_at=STAMP)
        session.add(point)
        session.flush()
        vehicle = session.get(m.Vehicle, vid)
        assert vehicle is not None
        vehicle.current_parking_location_id = point.id
        created["vehicles"] += 1
    if session.get(m.IntegrationState, integration_key) is None:
        session.add(m.IntegrationState(integration_key=integration_key, mode="polling", version=1,
                                       created_at=STAMP, updated_at=STAMP))
        created["integrations"] += 1
    session.flush()
    return created


def bootstrap_admin(session: Session) -> str:
    raw = _read_secret("BOOTSTRAP_ADMIN_MAX_ID_FILE")
    assert raw is not None
    if not re.fullmatch(r"[1-9][0-9]{0,18}", raw):
        raise ConfigError("BOOTSTRAP_ADMIN_MAX_ID_FILE: ожидается MAX user ID")
    name = os.environ.get("BOOTSTRAP_ADMIN_NAME", "Администратор автопарка").strip()[:200] or "Администратор"
    max_id = int(raw)
    emp = session.scalar(select(m.Employee).where(m.Employee.max_user_id == max_id))
    now = datetime.now(UTC)
    if emp is None:
        session.add(m.Employee(id=uuid.uuid4(), max_user_id=max_id, display_name=name, role="admin",
                               can_start_trip=True, version=1, created_at=now, updated_at=now))
        return "created"
    if emp.role != "admin":
        emp.role = "admin"
        emp.version += 1
        emp.updated_at = now
        return "promoted"
    return "exists"


def main(argv: list[str]) -> int:
    if len(argv) != 2 or argv[1] not in ("synthetic", "bootstrap-admin"):
        print(__doc__)
        return 2
    settings = Settings.from_env()
    if argv[1] == "synthetic" and settings.app_env == "production":
        print("seed: синтетические данные запрещены в production")
        return 2
    factory = make_session_factory(make_engine(settings))
    with unit_of_work(factory) as session:
        if argv[1] == "synthetic":
            print("seed:", apply_synthetic(session, os.environ.get("MAX_INTEGRATION_KEY", "demo-bot")))
        else:
            print("bootstrap-admin:", bootstrap_admin(session))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
