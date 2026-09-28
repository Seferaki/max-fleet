"""Подключение к PostgreSQL: один engine на процесс, Session на команду."""

from __future__ import annotations

import random
import time
from collections.abc import Callable, Iterator
from contextlib import contextmanager
from typing import TypeVar

from sqlalchemy import Engine, create_engine, text
from sqlalchemy.exc import DBAPIError, OperationalError
from sqlalchemy.orm import Session, sessionmaker

from app.config import Settings

T = TypeVar("T")

# SQLSTATE, при которых допустим повтор всей транзакции.
RETRYABLE_SQLSTATES = {"40001", "40P01"}


def make_engine(settings: Settings) -> Engine:
    url = settings.database_url
    if url.startswith("postgresql://"):
        url = "postgresql+psycopg://" + url[len("postgresql://"):]
    options = (
        f"-c statement_timeout={settings.db_statement_timeout_ms} "
        f"-c lock_timeout={settings.db_lock_timeout_ms} "
        "-c timezone=UTC"
    )
    return create_engine(
        url,
        pool_size=settings.db_pool_size,
        max_overflow=settings.db_max_overflow,
        pool_timeout=settings.db_pool_timeout,
        pool_pre_ping=True,
        connect_args={"options": options, "connect_timeout": 3},
    )


def make_session_factory(engine: Engine) -> sessionmaker[Session]:
    return sessionmaker(engine, expire_on_commit=False, autoflush=False)


def sqlstate(exc: BaseException) -> str | None:
    orig = getattr(exc, "orig", None)
    return getattr(orig, "sqlstate", None)


@contextmanager
def unit_of_work(factory: sessionmaker[Session]) -> Iterator[Session]:
    """Одна транзакция: commit на выходе, rollback при исключении."""
    session = factory()
    try:
        with session.begin():
            yield session
    finally:
        session.close()


def run_transaction(factory: sessionmaker[Session], work: Callable[[Session], T], *, attempts: int = 3) -> T:
    """Выполнить work в транзакции; deadlock/serialization — ограниченный повтор целиком."""
    for attempt in range(1, attempts + 1):
        try:
            with unit_of_work(factory) as session:
                return work(session)
        except DBAPIError as exc:
            if sqlstate(exc) in RETRYABLE_SQLSTATES and attempt < attempts:
                time.sleep(random.uniform(0.01, 0.05) * attempt)
                continue
            raise
    raise RuntimeError("unreachable")


def database_ok(engine: Engine) -> bool:
    try:
        with engine.connect() as conn:
            conn.execute(text("SELECT 1"))
        return True
    except (OperationalError, DBAPIError):
        return False
