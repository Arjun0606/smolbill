# smolbill

**The open-source usage-billing engine where your meter and your invoice provably never disagree.**

Single binary, Postgres-only, flat-priced, on top of Stripe, never a Merchant of Record. Apache-2.0.

> **The loop:** AI sets the rule → deterministic code does the math → a reconciliation ledger proves it never drifted.
>
> The AI passes *intent* (`attach_plan`), never money (`calculate_and_charge` is forbidden). A hallucinated decimal in billing ends a business relationship, so the AI never touches the math.

---

## Status: Phase 1 core + Postgres + HTTP API (done)

The provably-correct math layer is complete and runs as a real service: a single binary serving a `/v1` REST API, backed by Postgres (or in-memory for zero-setup). No Stripe or MCP yet — those are Phases 3 and 5.

### Quickstart

```sh
# zero-setup (in-memory, data not persisted):
go run ./cmd/smolbill serve              # listens on :8080

# with Postgres (schema auto-applied on boot):
DATABASE_URL='postgres://user@localhost:5432/smolbill?sslmode=disable' \
  go run ./cmd/smolbill serve

# or the whole stack in one command:
docker compose up
```

### `/v1` endpoints

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/customers` | create a customer |
| `POST` | `/v1/meters` | define a meter (`count\|sum\|max\|unique`) |
| `POST` | `/v1/plans` | versioned plan + prices |
| `POST` | `/v1/subscriptions` | attach a plan to a customer |
| `POST` | `/v1/events` | **idempotent** usage ingest (dedup on `idempotency_key`) |
| `GET`  | `/v1/usage/{customer_id}` | real-time usage + projected bill |
| `POST` | `/v1/invoices/preview` | **deterministic** exact invoice + verification hash |
| `POST` | `/v1/invoices/finalize` | materialize invoice + persist the reconciliation ledger |
| `GET`  | `/v1/reconcile/{invoice_id}` | **THE HEADLINE** — prove the invoice still agrees with the live event log |
| `POST` | `/v1/entitlements` | define a feature flag or metered allowance |
| `GET`  | `/v1/entitlements/{customer_id}` | real-time limit check (live usage, not a trusted counter) |
| `POST` | `/v1/alerts` | register a proactive spend alert (webhook at 50/80/100% of budget) |
| `POST` | `/v1/wallet/{customer_id}/topup` | idempotent prepaid wallet credit |
| `GET`  | `/v1/wallet/{customer_id}` | wallet balance + transactions |
| `GET`  | `/dashboard` | server-rendered admin dashboard (embedded in the binary) |
| `GET`  | `/dashboard/customers/{id}` | timeline, usage, invoices, wallet for a customer |
| `GET`  | `/dashboard/invoices/{id}/reconcile` | the reconciliation ledger, rendered |
| `GET`  | `/portal/{customer_id}` | **free embeddable customer portal** (usage, bill, wallet) |
| `GET`  | `/healthz` | liveness |

### MCP server (AI sets the rule)

`smolbill mcp` serves a Model Context Protocol server over stdio so an agent (Claude, Cursor) can configure billing in plain language:

```jsonc
// claude_desktop_config.json
{ "mcpServers": { "smolbill": { "command": "smolbill", "args": ["mcp"],
    "env": { "DATABASE_URL": "postgres://…" } } } }
```

Tools are **intent-only**: `create_meter`, `create_plan`, `attach_plan`, `set_spend_cap`, `get_usage`, `preview_invoice`. There is deliberately **no `charge()` or `calculate_bill()`** — the agent passes intent; the deterministic engine computes every cent. A test asserts no money-math tool can ever be exposed.

### Dashboard + free customer portal

The whole UI is server-rendered HTML embedded in the single binary (no Next.js, no build step, no framework) — visit `/dashboard`. The embeddable **customer portal** at `/portal/{id}` shows a customer their live usage, projected bill, wallet balance, and entitlement limits, with a subtle "metered by smolbill" footer. This is the feature Lago charges ~$1,500/mo for, given away in the OSS core (and a built-in distribution loop).

### Payments (Stripe) + spend alerts

- **Stripe** is a processor-agnostic adapter (`internal/payments`): `Processor` interface, a thin net/http `stripe.Client` (amounts sent as exact integer cents, idempotency keys on every call), and a `fake` test double. Set `STRIPE_SECRET_KEY` and `finalize` pushes the invoice to Stripe; unset, finalize is local-only. smolbill never holds funds or becomes a Merchant of Record.
- **Spend alerts** fire automatically on ingest: when a customer's projected current-period spend crosses each threshold of their budget, smolbill POSTs to the webhook — before the overage. Each threshold fires at most once per period (no spam).

### The reconciliation ledger (the demo)

Finalize an invoice, then ask `/v1/reconcile/{id}` whether it still holds:

```jsonc
// clean — HTTP 200
{ "verdict": "consistent", "hash_match": true, "stored_total": "3.00", "live_total": "3.00" }

// after a late event arrives post-finalize — HTTP 409
{ "verdict": "drift_detected", "hash_match": false,
  "stored_total": "3.00", "live_total": "8.00",
  "diffs": ["invoice total 3.00 -> 8.00 (5.00 drift)"],
  "lines": [{ "meter_code": "tokens",
    "diffs": ["raw event count 1 -> 2 (+1, likely late/out-of-order events)",
              "meter value 3000 -> 8000", "amount 3.00 -> 8.00"] }] }
```

The meter and the invoice can never *silently* disagree: at finalize we persist a ledger row per line (raw event count, meter value, line amount, verification hash); reconcile re-derives the same chain from current events and surfaces every difference.

### What's working underneath

- **Money** (`internal/money`) — exact decimal math, no floats; rounds **down** to the currency minor unit so we always under-bill rather than over-bill (the fail-safe rule).
- **Meters** (`internal/meter`) — `count | sum | max | unique` aggregation over a half-open `[start, end)` period, so adjacent periods never double-count a boundary event.
- **Pricing** (`internal/pricing`) — `flat | per_unit | tiered_graduated | tiered_volume`, with strict tier-ladder validation (malformed plans fail loudly, never silently mis-rate).
- **Invoice** (`internal/invoice`) — deterministic invoice assembly with time-exact proration of flat fees (usage is never prorated), plus a per-line meter trace and a SHA-256 verification hash. Same inputs → same invoice → same hash. This is the basis for the reconciliation ledger.
- **Ingest** (`internal/ingest`) — idempotent event acceptance on `idempotency_key` within a **published, configurable** dedup window; late / out-of-order events are accepted and attributed to their real `event_time`.
- **Store** (`internal/store`) — one interface, two backends: `memory` (tests, demo) and `postgres` (pgx, embedded schema applied on connect). The engine never depends on a concrete DB.
- **HTTP** (`internal/api`) — the `/v1` surface above, no web framework (net/http 1.22+ routing → single binary).
- **Reconcile** (`internal/reconcile`) — pure diff of stored ledger vs. live recompute; the proof behind `/v1/reconcile`.
- **Payments** (`internal/payments`) — processor-agnostic rail: `Processor` interface, `stripe` thin client (exact cents, idempotency), `fake` test double.
- **Alerts** (`internal/alerts`) — pure 50/80/100% threshold logic + webhook notifier; evaluated on every ingest.
- **Web** (`internal/api/web.go` + `templates/`) — server-rendered dashboard, reconcile view, and embeddable customer portal; HTML embedded via `go:embed` (single binary).
- **Engine** (`internal/engine`) — shared compute + plan-building used by both the REST API and the MCP server, so the two can never disagree.
- **MCP** (`internal/mcp`) — intent-only MCP server over stdio (no SDK); the agent sets rules, never touches money math.
- **Schema** (`migrations/0001_init.sql`) — the full v0 Postgres data model from the build plan §8.

## Run it

```sh
go test ./...                  # full suite (also: go test -race ./...)
go run ./cmd/smolbill demo     # end-to-end pipeline walkthrough (no DB)
go run ./cmd/smolbill serve    # the HTTP API
```

The `demo` creates a customer, a token meter, and a Pro plan ($49 base + graduated token pricing), ingests usage (including a duplicate key that gets ignored and a mid-period start that prorates the base fee), and prints an exact invoice with its meter→line trace and a determinism check.

Postgres integration tests run when `SMOLBILL_TEST_DATABASE_URL` is set; otherwise they skip so `go test ./...` is green with no DB.

## Design principles (non-negotiable)

1. **AI = intent, code = math.** The agent never calculates money.
2. **Event-sourced.** State is a replayable log; the invoice is always derivable from raw events.
3. **Idempotent everything.** Every ingest needs a key; the dedup window is documented.
4. **Fail safe = under-bill, never over-bill.**
5. **Reconciliation is the product**, not an afterthought.
6. **Postgres-only, single binary.** The simplicity is the differentiator.

## Roadmap

- **Phase 1 — deterministic core** ✅
- **Phase 1+ — Postgres backend + `/v1` HTTP API** ✅
- **Phase 2 — reconciliation ledger + entitlements** ✅ (`GET /reconcile/{invoice}`)
- **Phase 3 — Stripe push + spend alerts (50/80/100%)** ✅
- **Phase 4 — dashboard + free customer wallet/portal** ✅
- **Phase 5 — MCP server (thin, intent-only)** ✅
- **Phase 6 — single-binary release + Show HN** ← next

## License

Apache-2.0 (intentionally not AGPL).
