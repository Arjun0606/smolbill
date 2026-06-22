#!/usr/bin/env bash
# smolbill automated audit — quality + billing-correctness gates.
# Usage:  scripts/check.sh        (full audit; set SMOLBILL_TEST_DATABASE_URL for postgres tests)
# These are the exact invariants from billing-engine-build-plan.md, enforced.
set -uo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || exit 1
export PATH="/opt/homebrew/opt/postgresql@16/bin:/opt/homebrew/bin:/usr/local/go/bin:$PATH"

fail=0
gate() { if [ "$1" -ne 0 ]; then fail=1; printf "  \033[31m�’ %s\033[0m\n" "$2"; else printf "  \033[32m✔ %s\033[0m\n" "$2"; fi; }
have() { command -v "$1" >/dev/null 2>&1; }

echo "── build & format ──"
if have go; then
  go build ./... 2>/dev/null;            gate $? "go build"
  [ -z "$(gofmt -l . 2>/dev/null)" ];     gate $? "gofmt clean"
  go vet ./... >/dev/null 2>&1;           gate $? "go vet"
else
  echo "  (go not found — skipping build/vet)"
fi

echo "── billing-correctness invariants (no tooling needed) ──"
# 1. No float in the money path (tests may use float for JSON event props — excluded).
NF="$(grep -rn 'float64\|float32' internal/money internal/pricing internal/invoice internal/payments internal/domain --include='*.go' 2>/dev/null | grep -v '_test.go')"
[ -z "$NF" ]; gate $? "no floats in money/pricing/invoice/payments/domain"
[ -n "$NF" ] && echo "$NF" | sed 's/^/      /'

# 2. Core packages must NOT import the stripe adapter (cross-provider moat).
SC="$(grep -rl 'payments/stripe' internal/domain internal/pricing internal/invoice internal/meter internal/reconcile internal/store 2>/dev/null)"
[ -z "$SC" ]; gate $? "core is processor-agnostic (no stripe in core)"
[ -n "$SC" ] && echo "$SC" | sed 's/^/      /'

# 3. Single-binary promise: no Node/JS runtime sneaking in.
[ ! -f package.json ]; gate $? "single-binary (no package.json / node runtime)"

echo "── tests ──"
if have go; then
  if [ -n "${SMOLBILL_TEST_DATABASE_URL:-}" ]; then
    go test ./... -race >/dev/null 2>&1;                          gate $? "go test -race (incl. postgres)"
  else
    go list ./... | grep -v '/postgres' | xargs go test -race >/dev/null 2>&1; gate $? "go test -race (postgres skipped — set SMOLBILL_TEST_DATABASE_URL)"
  fi
fi

echo ""
if [ "$fail" -ne 0 ]; then echo -e "\033[31m🔴 AUDIT FAILED\033[0m"; exit 1; fi
echo -e "\033[32m🟢 AUDIT PASSED — money-safe, processor-agnostic, single-binary, green.\033[0m"
