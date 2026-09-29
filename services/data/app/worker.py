"""Фоновый data-worker.

    python -m app.worker run            # цикл обслуживания
    python -m app.worker ensure-bucket  # создать приватный bucket (однократно, в init-job)

Задачи цикла: освободить просроченные hold (дублирует проверку на командах),
удалить неподвязанные staged-фото старше срока, очистить устаревший idempotency-кэш.
Lease очередей возвращаются в обработку самим claim после истечения срока.
"""

from __future__ import annotations

import logging
import signal
import sys
import time
from typing import Any

from sqlalchemy import delete, select
from sqlalchemy.orm import Session, sessionmaker

from app import models as m
from app.config import Settings
from app.db import make_engine, make_session_factory, run_transaction
from app.domain.core import db_now, sweep_expired_holds, touch
from app.storage.object_store import ObjectStore, S3ObjectStore, StorageUnavailable

log = logging.getLogger("data-worker")
BATCH = 50


def cleanup_orphan_assets(factory: sessionmaker[Session], store: ObjectStore) -> int:
    """Удалить staged-объекты с истёкшим сроком, на которые нет ссылок."""

    def pick(session: Session) -> list[tuple[Any, str]]:
        now = db_now(session)
        rows = session.scalars(select(m.PhotoAsset).where(
            m.PhotoAsset.state == "staged", m.PhotoAsset.staging_expires_at < now)
            .order_by(m.PhotoAsset.staging_expires_at).limit(BATCH)
            .with_for_update(skip_locked=True)).all()
        picked = []
        for asset in rows:
            linked = session.scalar(select(m.InspectionPhoto.asset_id).where(
                m.InspectionPhoto.asset_id == asset.id)) or session.scalar(
                select(m.IssuePhoto.asset_id).where(m.IssuePhoto.asset_id == asset.id))
            if linked is not None:
                continue
            asset.state = "delete_pending"
            touch(asset, now)
            picked.append((asset.id, asset.object_key))
        return picked

    removed = 0
    for asset_id, key in run_transaction(factory, pick):
        try:
            store.delete(key)
        except StorageUnavailable:
            log.warning("orphan cleanup: хранилище недоступно, повтор позже")
            continue

        def mark(session: Session, ident: Any = asset_id) -> None:
            asset = session.get(m.PhotoAsset, ident, with_for_update=True)
            if asset is not None and asset.state == "delete_pending":
                now = db_now(session)
                asset.state = "deleted"
                asset.deleted_at = now
                touch(asset, now)

        run_transaction(factory, mark)
        removed += 1
    return removed


def cleanup_idempotency(factory: sessionmaker[Session]) -> int:
    def work(session: Session) -> int:
        now = db_now(session)
        ids = session.scalars(select(m.IdempotencyRecord.id).where(
            m.IdempotencyRecord.expires_at < now).limit(500)).all()
        if ids:
            session.execute(delete(m.IdempotencyRecord).where(m.IdempotencyRecord.id.in_(ids)))
        return len(ids)

    return run_transaction(factory, work)


def run_once(factory: sessionmaker[Session], store: ObjectStore) -> dict[str, int]:
    return {
        "holds_expired": run_transaction(factory, sweep_expired_holds, attempts=1),
        "orphans_removed": cleanup_orphan_assets(factory, store),
        "idempotency_purged": cleanup_idempotency(factory),
    }


def main(argv: list[str]) -> int:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    if len(argv) != 2 or argv[1] not in ("run", "ensure-bucket"):
        print(__doc__)
        return 2
    settings = Settings.from_env()
    store = S3ObjectStore(settings)
    if argv[1] == "ensure-bucket":
        store.ensure_bucket()
        log.info("bucket готов")
        return 0
    factory = make_session_factory(make_engine(settings))
    stopping = False

    def stop(*_: object) -> None:
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    while not stopping:
        try:
            stats = run_once(factory, store)
            if any(stats.values()):
                log.info("обслуживание: %s", stats)
        except Exception as exc:  # noqa: BLE001 — цикл не должен падать от временной ошибки
            log.warning("цикл обслуживания: %s", type(exc).__name__)
        for _ in range(10):
            if stopping:
                break
            time.sleep(1)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
