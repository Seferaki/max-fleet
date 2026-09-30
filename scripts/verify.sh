#!/bin/sh
set -eu

direction="${1:-all}"
case "$direction" in
    contract|gateway|web|docker|all) ;;
    *) echo "Направление: contract|gateway|web|docker|all" >&2; exit 2 ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

verify_contract() {
    cd "$repo_root"
    if ! python3 -c 'import yaml, jsonschema, openapi_spec_validator' >/dev/null 2>&1; then
        echo 'Установите зависимости: python3 -m pip install -r contracts/requirements-dev.txt' >&2
        return 1
    fi
    python3 contracts/validate.py
    npx --yes @redocly/cli@2.54.3 lint contracts/data-api.openapi.yaml contracts/map-api.openapi.yaml --config redocly.yaml --format=stylish
}

verify_gateway() {
    cd "$repo_root/services/gateway"
    if ! command -v go >/dev/null 2>&1; then
        echo 'Go CLI отсутствует. Нужен Go 1.27.1.' >&2
        return 1
    fi
    go test ./...
    go vet ./...
    go build ./cmd/gateway ./cmd/data-mock ./cmd/max-setup
}

verify_web() {
    cd "$repo_root/web"
    npm ci --no-audit --no-fund
    npm run typecheck
    npm test
    npm run build
}

verify_docker() {
    cd "$repo_root"
    docker compose -f deploy/compose.backend.yaml config --no-interpolate --quiet
    max_overlay_config=$(docker compose -f deploy/compose.full.yaml -f deploy/compose.full.max.yaml config --no-interpolate --format json)
    printf '%s' "$max_overlay_config" | grep -F '"MAX_BOT_TOKEN_FILE=/run/secrets/max_bot_token"' >/dev/null || {
        echo 'MAX Compose overlay must point MAX_BOT_TOKEN_FILE at its mounted Docker secret.' >&2
        return 1
    }
    printf '%s' "$max_overlay_config" | grep -F '"COMPANY_TIMEZONE=${COMPANY_TIMEZONE:-Europe/Moscow}"' >/dev/null || {
        echo 'Full Compose must pass COMPANY_TIMEZONE into the gateway.' >&2
        return 1
    }
    if ! docker info >/dev/null 2>&1; then
        echo 'Docker Engine недоступен. Контейнерные сборки не проверены.' >&2
        return 1
    fi
    docker build -f services/gateway/Dockerfile.gateway -t max-fleet-gateway:scaffold services/gateway
    docker build -f services/gateway/Dockerfile.data-mock -t max-fleet-data-mock:scaffold services/gateway
    docker build -f web/Dockerfile -t max-fleet-web:scaffold web
}

case "$direction" in contract|all) verify_contract ;; esac
case "$direction" in gateway|all) verify_gateway ;; esac
case "$direction" in web|all) verify_web ;; esac
case "$direction" in docker|all) verify_docker ;; esac
echo "verify $direction: OK"
