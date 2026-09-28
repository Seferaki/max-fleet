"""Alembic env: URL только из секрет-файла; advisory lock от параллельного запуска."""

from __future__ import annotations

import os
from pathlib import Path

from alembic import context
from sqlalchemy import create_engine, pool, text

from app.models import Base

MIGRATION_LOCK_ID = 7_331_001

config = context.config
target_metadata = Base.metadata


def migration_url() -> str:
    path = os.environ.get("MIGRATION_DATABASE_URL_FILE") or os.environ.get("DATABASE_URL_FILE")
    if not path:
        raise RuntimeError("MIGRATION_DATABASE_URL_FILE не задан")
    url = Path(path).read_text(encoding="utf-8").strip()
    if url.startswith("postgresql://"):
        url = "postgresql+psycopg://" + url[len("postgresql://"):]
    return url


def run_migrations_offline() -> None:
    context.configure(url="postgresql+psycopg://", target_metadata=target_metadata,
                      literal_binds=True, compare_type=True)
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    engine = create_engine(migration_url(), poolclass=pool.NullPool,
                           connect_args={"options": "-c timezone=UTC -c lock_timeout=10000"})
    with engine.connect() as connection:
        connection.execute(text("SELECT pg_advisory_lock(:id)"), {"id": MIGRATION_LOCK_ID})
        connection.commit()
        try:
            context.configure(connection=connection, target_metadata=target_metadata, compare_type=True)
            with context.begin_transaction():
                context.run_migrations()
            connection.commit()
        finally:
            connection.execute(text("SELECT pg_advisory_unlock(:id)"), {"id": MIGRATION_LOCK_ID})
            connection.commit()
    engine.dispose()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
