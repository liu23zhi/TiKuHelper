#!/usr/bin/env sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE_FILE=$SCRIPT_DIR/docker-compose.yml
QR_FILE=$SCRIPT_DIR/Wechat_qc_code.jpg
OVERRIDE_FILE=

cleanup() {
    exit_code=$?
    if [ -n "$OVERRIDE_FILE" ] && [ -f "$OVERRIDE_FILE" ]; then
        rm -f -- "$OVERRIDE_FILE"
    fi
    trap - EXIT HUP INT TERM
    exit "$exit_code"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if [ -f "$QR_FILE" ]; then
    if command -v mktemp >/dev/null 2>&1; then
        OVERRIDE_FILE=$(mktemp "${TMPDIR:-/tmp}/tikuhelper-compose-override.XXXXXX.yml") || exit 1
    else
        OVERRIDE_FILE=${TMPDIR:-/tmp}/tikuhelper-compose-override-$$.yml
        (set -C; : > "$OVERRIDE_FILE") || exit 1
    fi
    QR_FILE=$QR_FILE OVERRIDE_FILE=$OVERRIDE_FILE sh <<'EOF'
set -eu
escape_yaml_single_quote() {
    printf '%s' "$1" | sed "s/'/''/g"
}
escaped_qr=$(escape_yaml_single_quote "$QR_FILE")
cat > "$OVERRIDE_FILE" <<EOF_OVERRIDE
services:
  tikuhelper:
    volumes:
      - type: bind
        source: '$escaped_qr'
        target: /usr/share/nginx/html/Wechat_qc_code.jpg
        read_only: true
EOF_OVERRIDE
EOF
fi

if [ -n "$OVERRIDE_FILE" ]; then
    docker compose --file "$COMPOSE_FILE" --file "$OVERRIDE_FILE" "$@"
else
    docker compose --file "$COMPOSE_FILE" "$@"
fi
exit $?
