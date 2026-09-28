#!/usr/bin/env bash
# Резервная копия data-контура и проверка восстановления на новых томах (DE-08).
#   MAX_FLEET_SECRETS_DIR=... services/data/scripts/backup-restore.sh [каталог]
# Каталог по умолчанию — backups/<время> в корне репозитория (в .gitignore). Секреты не выводятся.
set -euo pipefail
export MSYS_NO_PATHCONV=1  # Git Bash на Windows: не переписывать /tmp/... в аргументах docker
cd "$(dirname "$0")/../../.."
: "${MAX_FLEET_SECRETS_DIR:?Set MAX_FLEET_SECRETS_DIR}"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
DIR=${1:-backups/$STAMP}
mkdir -p "$DIR"
ABS=$(cd "$DIR" && pwd -W 2>/dev/null || pwd)
SRC="docker compose -p max-fleet-data -f deploy/compose.data.yaml"
DST="docker compose -p max-fleet-restore -f deploy/compose.data.yaml"

echo "== 1. Кратко остановить writers и сделать согласованный снимок"
$SRC stop data-api data-worker
trap '$SRC start data-api data-worker >/dev/null 2>&1 || true' EXIT  # writers возвращаются при любой ошибке
$SRC exec -T postgres pg_dump -U postgres -d maxfleet -Fc -f /tmp/maxfleet.dump
$SRC cp postgres:/tmp/maxfleet.dump "$DIR/db.dump"
$SRC exec -T postgres rm -f /tmp/maxfleet.dump
$SRC run --rm --no-deps -v "$ABS:/backup" migrate python -m app.backup export-objects /backup
$SRC run --rm --no-deps migrate python -m app.backup verify > "$DIR/source-verify.json"
$SRC start data-api data-worker
trap - EXIT
sha256sum "$DIR/db.dump" "$DIR/manifest.json" > "$DIR/SHA256SUMS"

echo "== 2. Восстановить в пустой проект max-fleet-restore"
$DST down -v --remove-orphans >/dev/null 2>&1 || true
$DST up -d --wait postgres s3
$DST cp "$DIR/db.dump" postgres:/tmp/maxfleet.dump
$DST exec -T postgres pg_restore -U postgres -d maxfleet --exit-on-error /tmp/maxfleet.dump
$DST run --rm --no-deps -v "$ABS:/backup" migrate python -m app.backup import-objects /backup
$DST run --rm --no-deps migrate python -m app.backup verify > "$DIR/restore-verify.json"
$DST run --rm --no-deps migrate alembic current

echo "== 3. Сверка"
python - "$DIR/source-verify.json" "$DIR/restore-verify.json" <<'PY'
import json, sys
src, dst = (json.loads(open(p, encoding="utf-8").read().strip().splitlines()[-1]) for p in sys.argv[1:3])
assert src["tables"] == dst["tables"], "счётчики таблиц различаются"
assert dst["broken_photo_links"] == 0 and dst["objects_verified"] == src["objects_verified"]
print("OK: таблиц", len(dst["tables"]), "| строк", sum(dst["tables"].values()), "| объектов с совпавшим SHA-256", dst["objects_verified"])
PY
$DST down -v --remove-orphans
echo "Копия: $DIR"
