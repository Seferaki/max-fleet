#!/bin/sh
set -eu
umask 077

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
secret_directory="${MAX_FLEET_SECRETS_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/max-fleet/secrets}"
case "$secret_directory" in
    /*) ;;
    *) printf '%s\n' 'MAX_FLEET_SECRETS_DIR должен быть абсолютным путём.' >&2; exit 1 ;;
esac
mkdir -p "$secret_directory"
chmod 700 "$secret_directory"

python3 - "$secret_directory" "$repository_root" <<'PY'
import os
import secrets
import sys
from pathlib import Path

directory = Path(sys.argv[1]).resolve()
repository_root = Path(sys.argv[2]).resolve()
try:
    directory.relative_to(repository_root)
except ValueError:
    pass
else:
    raise SystemExit("MAX_FLEET_SECRETS_DIR должен быть вне репозитория")
names = (
    "max_webhook_secret", "data_api_token", "worker_api_token",
    "postgres_password", "data_runtime_password", "migration_password",
    "s3_access_key", "s3_secret_key",
)
for name in names:
    path = directory / name
    # Compose mounts file-backed secrets into non-root Go/Python containers.
    # The parent directory remains private (0700), so container-readable file
    # modes do not make the secrets readable to other host users.
    file_mode = 0o444
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        if path.is_symlink() or not path.is_file() or path.stat().st_size == 0:
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
