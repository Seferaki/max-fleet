#!/usr/bin/env bash
set -Eeuo pipefail

DOMAIN=""
WEB_PORT="8081"
EMAIL=""

usage() {
    cat <<'EOF'
Usage: sudo bash scripts/deploy-vds-nginx.sh --hostname example.org [--web-port 8081] [--email admin@example.org]

Issues a Let's Encrypt certificate with the HTTP-01 webroot challenge and
installs the public Nginx proxy. The default upstream is production web on
127.0.0.1:8081; do not point it at the loopback-only synthetic demo.
EOF
}

die() {
    printf 'deploy-vds-nginx: %s\n' "$*" >&2
    exit 1
}

while (($#)); do
    case "$1" in
        --hostname)
            (($# >= 2)) || die '--hostname needs a value'
            DOMAIN="$2"
            shift 2
            ;;
        --web-port)
            (($# >= 2)) || die '--web-port needs a value'
            WEB_PORT="$2"
            shift 2
            ;;
        --email)
            (($# >= 2)) || die '--email needs a value'
            EMAIL="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            usage >&2
            die "unknown option: $1"
            ;;
    esac
done

[[ "$(id -u)" == "0" ]] || die 'run as root'
[[ -n "$DOMAIN" ]] || die '--hostname is required'
[[ "${#DOMAIN}" -le 253 ]] || die 'hostname is too long'
[[ "$DOMAIN" == "${DOMAIN,,}" ]] || die 'hostname must be lowercase'
[[ "$DOMAIN" != *..* ]] || die 'hostname contains an empty label'
IFS='.' read -r -a labels <<< "$DOMAIN"
((${#labels[@]} >= 2)) || die 'hostname must be fully qualified'
for label in "${labels[@]}"; do
    [[ "$label" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] || die 'hostname contains an invalid DNS label'
done
[[ "$WEB_PORT" =~ ^[0-9]{1,5}$ ]] || die 'web port must be numeric'
((10#$WEB_PORT >= 1 && 10#$WEB_PORT <= 65535)) || die 'web port is outside 1..65535'
if [[ -n "$EMAIL" ]]; then
    [[ "$EMAIL" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] || die 'email format is invalid'
fi

for command in curl certbot nginx systemctl getent install mktemp sed; do
    command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
done

WEB="http://127.0.0.1:${WEB_PORT}"
if ! readiness="$(curl --fail --silent --show-error --max-time 5 "$WEB/health/ready")"; then
    die 'production web readiness is unavailable on the configured loopback port'
fi
[[ "$readiness" == *'"status":"static_ready"'* ]] || die 'upstream is not the expected React web service'
getent ahostsv4 "$DOMAIN" >/dev/null || die 'hostname has no IPv4 DNS record from this server'

ACME_ROOT=/var/www/max-fleet-acme
CONFIG=/etc/nginx/conf.d/max-fleet.conf
TEMPLATE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../deploy/nginx" && pwd)/max-fleet-vds.conf.template"
[[ -f "$TEMPLATE" ]] || die "Nginx template not found: $TEMPLATE"
install -d -o root -g root -m 0755 "$ACME_ROOT/.well-known/acme-challenge"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP="${CONFIG}.backup.${STAMP}"
TEMP=""
PROBE=""

cleanup() {
    [[ -z "$TEMP" || ! -e "$TEMP" ]] || rm -f -- "$TEMP"
    [[ -z "$PROBE" || ! -e "$PROBE" ]] || rm -f -- "$PROBE"
}
trap cleanup EXIT

restore_previous() {
    if [[ -e "$BACKUP" ]]; then
        install -m 0644 "$BACKUP" "$CONFIG"
    else
        rm -f -- "$CONFIG"
    fi
    nginx -t && systemctl reload nginx
}

install_candidate() {
    local content="$1"
    TEMP="$(mktemp /etc/nginx/conf.d/.max-fleet.conf.XXXXXX)"
    printf '%s\n' "$content" > "$TEMP"
    chmod 0644 "$TEMP"
    mv -f -- "$TEMP" "$CONFIG"
    TEMP=""
    if ! nginx -t; then
        restore_previous || true
        die 'Nginx validation failed; previous config restored'
    fi
    if ! systemctl reload nginx; then
        restore_previous || true
        die 'Nginx reload failed; previous config restored'
    fi
}

FULLCHAIN="/etc/letsencrypt/live/${DOMAIN}/fullchain.pem"
PRIVATE_KEY="/etc/letsencrypt/live/${DOMAIN}/privkey.pem"
if [[ ! -s "$FULLCHAIN" || ! -s "$PRIVATE_KEY" ]]; then
    if [[ -e "$CONFIG" ]]; then
        cp -a -- "$CONFIG" "$BACKUP"
    fi

    bootstrap_config="$(cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name ${DOMAIN};
    location ^~ /.well-known/acme-challenge/ {
        root ${ACME_ROOT};
        default_type text/plain;
        try_files \$uri =404;
    }
    location / { return 404; }
}
EOF
)"
    install_candidate "$bootstrap_config"

    probe_name="max-fleet-acme-probe-${STAMP}-$$"
    PROBE="${ACME_ROOT}/.well-known/acme-challenge/${probe_name}"
    printf '%s' "$probe_name" > "$PROBE"
    chmod 0644 "$PROBE"
    served="$(curl --fail --silent --show-error --max-time 10 -H "Host: ${DOMAIN}" "http://127.0.0.1/.well-known/acme-challenge/${probe_name}")" || die 'local HTTP-01 route check failed'
    [[ "$served" == "$probe_name" ]] || die 'HTTP-01 probe returned unexpected content'
    rm -f -- "$PROBE"
    PROBE=""

    certbot_args=(certonly --webroot --webroot-path "$ACME_ROOT" --domain "$DOMAIN" --non-interactive --agree-tos --preferred-challenges http)
    if [[ -n "$EMAIL" ]]; then
        certbot_args+=(--email "$EMAIL" --no-eff-email)
    else
        certbot_args+=(--register-unsafely-without-email)
    fi
    certbot "${certbot_args[@]}"
fi

[[ -s "$FULLCHAIN" && -s "$PRIVATE_KEY" ]] || die 'trusted certificate files are not available'
if [[ ! -e "$BACKUP" && -e "$CONFIG" ]]; then
    cp -a -- "$CONFIG" "$BACKUP"
fi
rendered="$(sed -e "s|__MAX_FLEET_PUBLIC_HOSTNAME__|${DOMAIN}|g" \
    -e "s|__MAX_FLEET_WEB_PORT__|${WEB_PORT}|g" "$TEMPLATE")"
install_candidate "$rendered"

ready="$(curl --fail --silent --show-error --max-time 15 --resolve "${DOMAIN}:443:127.0.0.1" "https://${DOMAIN}/health/ready")" || {
    restore_previous || true
    die 'TLS readiness check failed; previous config restored'
}
[[ "$ready" == *'"status":"static_ready"'* ]] || {
    restore_previous || true
    die 'TLS readiness returned an unexpected response; previous config restored'
}

printf 'nginx_tls_route=ready hostname=%s upstream=127.0.0.1:%s backup=%s\n' "$DOMAIN" "$WEB_PORT" "$BACKUP"
