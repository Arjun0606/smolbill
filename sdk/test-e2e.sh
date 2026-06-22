#!/usr/bin/env bash
# End-to-end SDK proof: boot a real smolbill binary (in-memory) and run the
# TypeScript and Python integration suites against it. This is what verifies the
# SDKs actually drive the live API correctly — not just that they compile.
#
#   sdk/test-e2e.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-8099}"
export ADDR=":$PORT"
export SMOLBILL_BASE_URL="http://127.0.0.1:$PORT"
BIN="$(mktemp -d)/smolbill-e2e"

# Refuse to run against a port that's already in use: a stale server would serve
# its OWN accumulated state (and globally-seen idempotency keys would silently
# dedup this run's events away), producing confusing false failures.
if lsof -ti "tcp:$PORT" >/dev/null 2>&1; then
  echo "port $PORT is already in use — stop the process on it (lsof -ti tcp:$PORT) or set PORT=<free>"
  exit 1
fi

echo "── building smolbill binary ──"
go build -o "$BIN" ./cmd/smolbill

echo "── starting server on $ADDR (in-memory) ──"
"$BIN" serve &
SRV=$!
trap 'kill "$SRV" 2>/dev/null || true' EXIT

# Wait for liveness (up to ~5s).
for _ in $(seq 1 50); do
  if curl -fsS "$SMOLBILL_BASE_URL/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "$SMOLBILL_BASE_URL/healthz" >/dev/null || { echo "server failed to start"; exit 1; }

echo "── TypeScript SDK (unit + integration) ──"
( cd sdk/typescript && node --test --experimental-strip-types test/*.test.ts )

echo "── Python SDK (unit + integration) ──"
( cd sdk/python && python3 -m unittest discover -s tests )

echo ""
echo "🟢 SDK E2E GREEN — both clients drove the full lifecycle against a live binary."
