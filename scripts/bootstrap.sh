#!/bin/sh
set -eu
umask 077

secret_directory="${XDG_DATA_HOME:-$HOME/.local/share}/max-fleet/secrets"
mkdir -p "$secret_directory"
chmod 700 "$secret_directory"

python3 - "$secret_directory" <<'PY'
import os
import secrets
import sys
from pathlib import Path

directory = Path(sys.argv[1])
names = (
    "max_webhook_secret", "data_api_token", "worker_api_token",
    "postgres_password", "data_runtime_password", "migration_password",
    "s3_access_key", "s3_secret_key",
)
for name in names:
    path = directory / name
    file_mode = 0o444 if name in {"data_api_token", "worker_api_token"} else 0o600
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        if not path.is_file() or path.stat().st_size == 0:
            raise SystemExit(f"Пустой или неверный secret-файл: {name}")
        path.chmod(file_mode)
        print(f"{name}: уже существует")
        continue
    with os.fdopen(descriptor, "w", encoding="ascii") as output:
        output.write(secrets.token_hex(32))
    path.chmod(file_mode)
    print(f"{name}: создан")
print("MAX token вводится владельцем локально, не в чат")
PY
