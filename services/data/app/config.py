"""Конфигурация data-api.

Секреты читаются только из файлов (*_FILE), значения никогда не логируются.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path

CONTRACT_VERSION = "1.0"


class ConfigError(RuntimeError):
    pass


def _read_secret(env_name: str, *, required: bool = True) -> str | None:
    path = os.environ.get(env_name, "").strip()
    if not path:
        if required:
            raise ConfigError(f"{env_name} не задан")
        return None
    try:
        value = Path(path).read_text(encoding="utf-8").strip()
    except OSError as exc:
        raise ConfigError(f"{env_name}: файл недоступен") from exc
    if not value or "\n" in value or "\r" in value:
        raise ConfigError(f"{env_name}: пустой или многострочный секрет")
    return value


def _int(env_name: str, default: int) -> int:
    raw = os.environ.get(env_name)
    if raw is None or raw == "":
        return default
    try:
        return int(raw)
    except ValueError as exc:
        raise ConfigError(f"{env_name} должен быть целым") from exc


@dataclass(frozen=True)
class Settings:
    database_url: str
    data_api_token: str
    worker_api_token: str
    s3_endpoint: str | None
    s3_bucket: str
    s3_region: str
    s3_access_key: str | None
    s3_secret_key: str | None
    app_env: str = "development"
    build_sha: str = "unknown"
    company_timezone: str = "Europe/Moscow"
    db_pool_size: int = 5
    db_max_overflow: int = 5
    db_pool_timeout: int = 3
    db_statement_timeout_ms: int = 3000
    db_lock_timeout_ms: int = 1000
    hold_minutes: int = 15
    challenge_minutes: int = 5
    stage_asset_minutes: int = 30
    max_upload_bytes: int = 10 * 1024 * 1024
    max_pixels: int = 25_000_000
    extra: dict[str, str] = field(default_factory=dict)

    @classmethod
    def from_env(cls) -> Settings:
        data_token = _read_secret("DATA_API_TOKEN_FILE")
        worker_token = _read_secret("WORKER_API_TOKEN_FILE")
        assert data_token is not None and worker_token is not None
        if data_token == worker_token:
            raise ConfigError("DATA_API_TOKEN и WORKER_API_TOKEN должны различаться")
        database_url = _read_secret("DATABASE_URL_FILE")
        assert database_url is not None
        return cls(
            database_url=database_url,
            data_api_token=data_token,
            worker_api_token=worker_token,
            s3_endpoint=os.environ.get("S3_ENDPOINT") or None,
            s3_bucket=os.environ.get("S3_BUCKET", "max-fleet-photos"),
            s3_region=os.environ.get("S3_REGION", "us-east-1"),
            s3_access_key=_read_secret("S3_ACCESS_KEY_FILE", required=False),
            s3_secret_key=_read_secret("S3_SECRET_KEY_FILE", required=False),
            app_env=os.environ.get("APP_ENV", "development"),
            build_sha=os.environ.get("BUILD_SHA", "unknown")[:64],
            company_timezone=os.environ.get("COMPANY_TIMEZONE", "Europe/Moscow"),
            db_pool_size=_int("DB_POOL_SIZE", 5),
            db_max_overflow=_int("DB_MAX_OVERFLOW", 5),
            db_pool_timeout=_int("DB_POOL_TIMEOUT", 3),
            db_statement_timeout_ms=_int("DB_STATEMENT_TIMEOUT_MS", 3000),
            db_lock_timeout_ms=_int("DB_LOCK_TIMEOUT_MS", 1000),
        )
