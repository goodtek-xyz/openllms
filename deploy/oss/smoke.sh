#!/usr/bin/env bash
# Local OSS smoke without Docker: builds gateway, runs SQLite+file vault, hits /health, /ready, /livez and /readyz.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'kill ${GW_PID:-0} 2>/dev/null || true; rm -rf "$TMP"' EXIT

export GOTOOLCHAIN=local
go build -o "$TMP/llms-gateway" "$ROOT/cmd/llms-gateway"

export DATABASE_URL="sqlite:$TMP/llms.db"
export LLMS_SECRETS_DIR="$TMP/secrets"
export BOOTSTRAP_TOKEN="smoke-bootstrap"
export HTTP_ADDR="127.0.0.1:18082"
unset INFISICAL_PROJECT_ID INFISICAL_CLIENT_ID INFISICAL_CLIENT_SECRET || true

"$TMP/llms-gateway" >"$TMP/gw.log" 2>&1 &
GW_PID=$!

for _ in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:18082/ready" >/dev/null; then
    break
  fi
  sleep 0.2
done

curl -sf "http://127.0.0.1:18082/health" | grep -q '"status":"ok"'
curl -sf "http://127.0.0.1:18082/livez" | grep -q '"status":"ok"'
curl -sf "http://127.0.0.1:18082/readyz" | grep -q '"status":"ready"'
curl -sf "http://127.0.0.1:18082/ready" | grep -q '"status":"ready"'

# Unit/integration E2E covers chat path; this script verifies process boot.
echo "oss smoke ok (sqlite+file secrets)"
