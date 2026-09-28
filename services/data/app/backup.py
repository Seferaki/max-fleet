"""Резервирование объектов S3 и проверка восстановления (DE-08).

    python -m app.backup export-objects /backup   # объекты + manifest.json с SHA-256
    python -m app.backup import-objects /backup   # вернуть объекты в пустой bucket
    python -m app.backup verify                   # FK/счётчики + чтение и хэш каждого сохранённого фото

Дамп PostgreSQL делается pg_dump в контейнере postgres; этот модуль отвечает за файлы
и сверку. Каталог backup находится вне Git; содержимое не логируется.
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

from sqlalchemy import func, select, text

from app import models as m
from app.config import Settings
from app.db import make_engine, make_session_factory
from app.storage.object_store import S3ObjectStore


def _stored_assets(session: object) -> list[m.PhotoAsset]:
    return list(session.scalars(select(m.PhotoAsset).where(  # type: ignore[attr-defined]
        m.PhotoAsset.stored_at.is_not(None), m.PhotoAsset.state.in_(("staged", "ready")))
        .order_by(m.PhotoAsset.id)).all())


def export_objects(target: Path, settings: Settings) -> dict[str, int]:
    store = S3ObjectStore(settings)
    factory = make_session_factory(make_engine(settings))
    target.mkdir(parents=True, exist_ok=True)
    (target / "objects").mkdir(exist_ok=True)
    manifest: dict[str, dict[str, str]] = {}
    with factory() as session:
        for asset in _stored_assets(session):
            data = store.get(asset.object_key)
            digest = hashlib.sha256(data).hexdigest()
            if digest != asset.sha256:
                raise SystemExit(f"backup: хэш объекта не совпадает с БД ({asset.id})")
            (target / "objects" / str(asset.id)).write_bytes(data)
            manifest[str(asset.id)] = {"object_key": asset.object_key, "sha256": digest,
                                       "mime_type": asset.mime_type}
    (target / "manifest.json").write_text(json.dumps(manifest, indent=1), encoding="utf-8")
    return {"objects": len(manifest)}


def import_objects(source: Path, settings: Settings) -> dict[str, int]:
    store = S3ObjectStore(settings)
    store.ensure_bucket()
    manifest = json.loads((source / "manifest.json").read_text(encoding="utf-8"))
    for asset_id, item in manifest.items():
        data = (source / "objects" / asset_id).read_bytes()
        if hashlib.sha256(data).hexdigest() != item["sha256"]:
            raise SystemExit(f"restore: повреждён файл резервной копии ({asset_id})")
        store.put(item["object_key"], data, item["mime_type"])
    return {"objects": len(manifest)}


def verify(settings: Settings) -> dict[str, object]:
    store = S3ObjectStore(settings)
    engine = make_engine(settings)
    factory = make_session_factory(engine)
    counts: dict[str, int] = {}
    with factory() as session:
        for table in m.Base.metadata.sorted_tables:
            counts[table.name] = session.scalar(select(func.count()).select_from(table)) or 0
        broken_links = session.scalar(text(
            "SELECT count(*) FROM inspection_photos ip LEFT JOIN photo_assets pa ON pa.id = ip.asset_id "
            "WHERE pa.id IS NULL")) or 0
        checked = 0
        for asset in _stored_assets(session):
            if hashlib.sha256(store.get(asset.object_key)).hexdigest() != asset.sha256:
                raise SystemExit(f"verify: объект не совпадает ({asset.id})")
            checked += 1
    return {"tables": counts, "broken_photo_links": broken_links, "objects_verified": checked}


def main(argv: list[str]) -> int:
    settings = Settings.from_env()
    if len(argv) == 3 and argv[1] == "export-objects":
        print(json.dumps(export_objects(Path(argv[2]), settings)))
    elif len(argv) == 3 and argv[1] == "import-objects":
        print(json.dumps(import_objects(Path(argv[2]), settings)))
    elif len(argv) == 2 and argv[1] == "verify":
        print(json.dumps(verify(settings), ensure_ascii=False))
    else:
        print(__doc__)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
