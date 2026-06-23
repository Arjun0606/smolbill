# smolbill

[![license](https://img.shields.io/badge/engine-AGPLv3-F5A623)](LICENSE) [![sdks](https://img.shields.io/badge/SDKs-Apache--2.0-8E8E8E)](sdk/) [![go](https://img.shields.io/badge/Go-single%20binary-00ADD8)](cmd/smolbill) [![status](https://img.shields.io/badge/billing-provably%20correct-F5A623)](#what-it-does)

**Open-source, provably-correct usage billing. One Go binary and a Postgres. Flat-priced, never a percent of your revenue.**

smolbill meters usage, issues correct invoices, and can *prove* — line by line, from the raw events — that your meter and your invoice never silently disagree. It collects through whatever payment processor you already use and never holds your money. An AI agent can run the whole thing in plain English, while the deterministic engine does every cent.

<p align="center">
  <img src="assets/demo.gif" alt="smolbill computes a real invoice, then proves the meter and the invoice agree with a hash" width="760">
  <br>
  <em>One command. It computes a real invoice, then <strong>proves</strong> the meter and the invoice agree — with a hash.</em>
</p>

---

## Why it exists

Billing is the one system that converts your product into money, and the one system where a wrong number is not a bug report — it's a refund, a dispute, a churned customer, or a quiet revenue leak you find months later. Most billing stacks are built so that you can never actually *prove* a given invoice is right; you re-check it by exporting usage into a warehouse and reconciling in a spreadsheet. The numbers leak, and nobody can say by how much.

smolbill is built from four convictions:

- **Correctness is the foundation, not a feature.** Money is integer-precise decimal math, never floating point, and rounds toward under-billing so an error can never bill a customer *more* than they owe. Every invoice is derived from an event log and carries a hash, so the meter and the invoice can be re-derived and compared at any time. Drift is detectable, quantifiable, and loud — not silent.

- **Small enough to audit, small enough to run yourself.** One static Go binary and a Postgres. No Kafka, no ClickHouse, no stream processor, no workflow engine, no data pipeline to babysit. The thing that decides what your customers owe should be something one person can read end to end and operate without a platform team.

- **You own it, and you own your money.** Self-host it, read it, fork it. It collects through your own processor and never becomes the merchant of record, so it never sits on your funds and a processor freeze can't take your billing logic down with it. Flat-priced — it never takes a cut of your revenue.

- **AI removes the learning curve, not the safety.** An agent operates the whole system in plain English over MCP, so there's no API to learn and no docs to read. But the agent only ever passes *intent* — it never does the arithmetic, and the few tools that move real money stay in preview until a human arms them. The agent cannot arm itself. A hallucinated decimal can end a business relationship, so the model is structurally kept away from the math and the money.

## What it does

A single static binary (Postgres, or in-memory for zero-setup) that gives you:

- **Provably-correct metering** — idempotent event ingest with a documented dedup window; late and out-of-order events re-rate correctly instead of silently being wrong.
- **A reconciliation ledger** — `GET /v1/reconcile/{invoice}` recomputes the invoice from the live event log and tells you, line by line, whether anything drifted (200 if consistent, 409 + a diff if not). `GET /v1/reconcile` does it across the whole account and reports the total money at risk — revenue leakage made provable, in one call.
- **Revenue recognition (ASC 606)** — `GET /v1/invoices/{invoice}/revenue?as_of=…` returns the recognized-vs-deferred split, straight-line over the service period, computed fresh from the invoice so it never drifts.
- **Real-time enforcement** — `POST /v1/gate` answers "may this customer do this *right now*?" synchronously: a hard allow/deny computed live against a metered entitlement (feature + quantity) and/or the prepaid wallet (cost). Call it in your request hot path to **block at the limit or at balance = 0**. Spend caps only warn; the gate denies.
- **A from-the-ground-up AI surface** — your agent configures and operates billing by passing *intent* over MCP, and can **simulate a pricing change against your real usage** (`/v1/invoices/simulate`) before committing anything. The deterministic engine does every cent; the model never touches the math.
- **A decoupled payment rail** — invoicing and charges only, behind a processor-agnostic interface. smolbill never holds funds or becomes a merchant of record, so a processor freeze can't take down your billing logic.
- **A free embeddable customer portal + prepaid wallet** — in the open-source core, not a paid add-on.
- **Decline-aware dunning** — failed-payment recovery with a *transparent, configurable* retry cadence (not a black box) that routes by decline reason: soft declines retry on a research-grounded schedule; a dead card or an authentication challenge stops immediately instead of burning attempts. The customer-facing copy is yours to read, edit, and preview.
- Flat-priced, **never a percent of your revenue.** Open core under AGPLv3; permissive (Apache-2.0) client SDKs; commercial license if AGPL doesn't fit.

Money math is integer-precise decimals (never floats) and rounds toward under-billing. The HTTP API and the MCP server compute through one shared engine, so a human and an agent can never get different numbers.

### Quickstart

**See everything working in 60 seconds — no setup, no config, no account:**

```sh
go run ./cmd/smolbill quickstart
```

It seeds a real account (a plan, usage events, a finalized invoice) and serves it, then prints links to the dashboard, the reconciliation ledger, live usage, **time-travel** (`?as_of=`), and the **AI sandbox** (`/simulate`) — all populated and clickable.

Then run it for real:

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
| `GET`  | `/v1/usage/{customer_id}` | real-time usage + projected bill — add `?as_of=<RFC3339>` to **time-travel** the bill to a past moment (before late events arrived) |
| `POST` | `/v1/invoices/preview` | **deterministic** exact invoice + verification hash |
| `POST` | `/v1/invoices/simulate` | **sandbox** — replay real usage against a *proposed* plan, diff vs the live bill, commit nothing |
| `POST` | `/v1/invoices/finalize` | materialize invoice + persist the reconciliation ledger |
| `GET`  | `/v1/reconcile` | **account-wide drift scan** — reconcile every finalized invoice, report drift + money at risk |
| `GET`  | `/v1/reconcile/{invoice_id}` | **THE HEADLINE** — prove one invoice still agrees with the live event log |
| `GET`  | `/v1/invoices/{invoice_id}/verify` | **cross-boundary** — prove the ledger equals what the payment processor actually billed |
| `POST` | `/v1/invoices/{invoice_id}/collect` | **dunning** — attempt collection now (manual retry, e.g. after a card update) |
| `GET`  | `/v1/invoices/{invoice_id}/collection` | inspect recovery state: every attempt, the decline reason, the next retry time |
| `POST` | `/v1/dunning/run` | process every **due** collection — the endpoint a cron hits on a cadence |
| `GET`  | `/v1/analytics` | account snapshot: revenue by currency, subscriptions, dunning recovery (computed live, never a cached counter) |
| `GET`  | `/v1/dunning/templates` | the customer-facing dunning copy (your override or the default) — **yours to read and edit** |
| `PUT`  | `/v1/dunning/templates/{event}` | override the copy for one event (validated to render before saving) |
| `POST` | `/v1/dunning/preview` | **live preview** a rendered dunning message |
| `POST` | `/v1/entitlements` | define a feature flag or metered allowance |
| `GET`  | `/v1/entitlements/{customer_id}` | real-time limit check (live usage, not a trusted counter) |
| `POST` | `/v1/alerts` | register a proactive spend alert (webhook at 50/80/100% of budget) |
| `POST` | `/v1/webhooks` | register a **signed** webhook for `invoice.finalized` / `drift.detected` (HMAC-SHA256, secret returned once) |
| `GET`  | `/v1/webhooks` | list registered webhooks |
| `POST` | `/v1/wallet/{customer_id}/topup` | idempotent prepaid wallet credit |
| `GET`  | `/v1/wallet/{customer_id}` | wallet balance + transactions |
| `GET`  | `/dashboard` | server-rendered admin dashboard (embedded in the binary) |
| `GET`  | `/dashboard/customers/{id}` | timeline, usage, invoices, wallet for a customer |
| `GET`  | `/dashboard/invoices/{id}/reconcile` | the reconciliation ledger, rendered |
| `GET`  | `/portal/{customer_id}` | **free embeddable customer portal** (usage, bill, wallet) |
| `GET`  | `/healthz` | liveness |

### SDKs (TypeScript + Python)

Official, **dependency-free** clients in [`sdk/typescript`](sdk/typescript) and [`sdk/python`](sdk/python). Five lines to a metered bill:

```ts
const sb = new Smolbill({ baseUrl: "http://localhost:8080" });
const cus = await sb.customers.create({ name: "Acme AI" });
await sb.events.ingest({ idempotency_key: "req-1", customer_id: cus.id, meter_code: "tokens", properties: { n: 1000 } });
```

Both expose typed, signature-verifying webhook handling (`verifyWebhook` / `verify_webhook`) for the signed `invoice.finalized` and `drift.detected` deliveries. Three guarantees keep them honest:

- **Snake_case 1:1 with the wire** — no field-mapping layer to drift.
- **A drift guard** — [`sdk/routes.json`](sdk/routes.json) is the contract the SDKs implement, and a Go test (`TestRoutesMatchSDKManifest`) fails if the server registers a route the manifest doesn't list, so a new endpoint can't ship without the clients catching up.
- **A cross-language signing test** — Go, TypeScript, and Python all assert the **same golden HMAC digests**, proving a webhook signed by the server verifies identically in every client.

`sdk/test-e2e.sh` boots a real binary and drives the full billing lifecycle through both SDKs.

### Run your billing by describing it (MCP)

smolbill is built on MCP from the ground up, not bolted on. Connect an agent (Claude, Cursor) in **one command** — `npx`, not a hand-edited path:

```jsonc
// claude_desktop_config.json
{ "mcpServers": { "smolbill": { "command": "npx", "args": ["-y", "smolbill-mcp"],
    "env": { "DATABASE_URL": "postgres://…" } } } }   // omit DATABASE_URL for in-memory
```

Then just **say what you want** and it's done:

> *"set up usage billing for my AI app — $0.001 per token with a $20 monthly base"*
> *"is invoice inv_4f… correct? prove it against the events"*
> *"what would I bill Acme if I moved them to the new tiered plan?"*

The agent maps each request to the right tool and shows you the result. You have a working plan and a previewable invoice in one step.

**Works with any MCP client, local or remote.** The default `smolbill mcp` speaks the **stdio** transport (Claude Code/Desktop, Cursor, Cline, Windsurf, Zed, Continue). For hosted clients, `smolbill mcp --http` serves the **Streamable HTTP** transport on `ADDR` at `/mcp`. The HTTP transport is API-key authenticated (the same keys as `/v1`), and the server negotiates the MCP protocol version by echoing the client's requested revision.

The agent can drive **every feature**, not just setup:

- **configure:** `create_customer`, `create_meter`, `create_plan`, `attach_plan`, `create_entitlement`, `create_webhook`, `set_spend_cap`
- **operate:** `record_usage`, `finalize_invoice`, `topup_wallet`, `collect_invoice`, `run_dunning`
- **observe / prove:** `list_customers`, `list_plans`, `list_subscriptions`, `list_invoices`, `list_meters`, `list_webhooks`, `get_usage` (with `as_of` time-travel), `preview_invoice`, `simulate_plan_change`, `reconcile_invoice`, `scan_billing_drift`, `verify_invoice`, `check_entitlement`, `get_wallet`, `get_collection`, `get_analytics`

**No learning curve.** On connect, the agent reads smolbill's `instructions` (returned in the MCP `initialize` result) and calls `get_started` to orient itself — so it knows the flows, the safety rules, and your account's next step before you ask. You never read these docs; the agent already did.

**Safe by default.** Reads and configuration just happen. The three tools that move real money (`collect_invoice`, `run_dunning`, `topup_wallet`) stay in **preview** until the operator arms the engine (`SMOLBILL_MCP_ARMED=true`) — the agent cannot arm itself, so a confused or hijacked agent can never push money on its own, and an optional per-operation cap backstops it. And there is deliberately **no `charge()` or `calculate_bill()` tool** — the agent passes intent; the deterministic engine computes every cent. A test asserts no money-math tool can ever be exposed.

### Dashboard + free customer portal

The whole UI is server-rendered HTML embedded in the single binary (no Next.js, no build step, no framework) — visit `/dashboard`. The embeddable **customer portal** at `/portal/{id}` shows a customer their live usage, projected bill, wallet balance, and entitlement limits, with a subtle "metered by smolbill" footer — a built-in distribution loop, in the open-source core.

### Payments (bring any processor) + spend alerts

- **Any processor, one interface.** `internal/payments` defines a small `Processor` interface (`PushInvoice` / `FetchInvoice` / `ChargeInvoice`); the engine depends only on it and never imports a concrete rail. Adapters ship for **Stripe, Dodo, Paddle, Lemon Squeezy, Polar, Creem, Razorpay, and a non-custodial crypto/stablecoin rail** — each a thin net/http client (amounts sent as exact integer minor units, idempotency keys on every call), plus a `fake` test double.
- **Selection is environment-driven.** Set `SMOLBILL_PROCESSOR=…`, or just set one provider's credentials and smolbill auto-detects it. With a rail configured, `finalize` pushes the invoice to it; unset, finalize is local-only (the ledger is still written). smolbill never holds funds or becomes a merchant of record for *your* customers.
- **Merchant-of-record rails self-dunn.** Card rails support off-session retries (smolbill drives dunning). Merchant-of-record rails and the push-only crypto rail manage collection themselves, so smolbill's `ChargeInvoice` surfaces an explicit error there rather than mis-routing it as a card decline.
- Adding a rail is one file behind the interface + one line in `internal/payments/providers`.
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

The meter and the invoice can never *silently* disagree: at finalize we persist a ledger row per line (raw event count, meter value, line amount, verification hash); reconcile re-derives the same chain from current events and surfaces every difference. `GET /v1/reconcile` (no id) runs this across every finalized invoice and totals the money at risk.

### What's working underneath

- **Money** (`internal/money`) — exact decimal math, no floats; rounds **down** to the currency minor unit so we always under-bill rather than over-bill (the fail-safe rule).
- **Meters** (`internal/meter`) — `count | sum | max | unique` aggregation over a half-open `[start, end)` period, so adjacent periods never double-count a boundary event.
- **Pricing** (`internal/pricing`) — `flat | per_unit | tiered_graduated | tiered_volume | package | percentage`, each with optional **minimum spend (commitment)** and **maximum cap**, and strict tier-ladder validation (malformed plans fail loudly, never silently mis-rate).
- **Invoice** (`internal/invoice`) — deterministic invoice assembly with time-exact proration of flat fees (usage is never prorated), plus a per-line meter trace and a SHA-256 verification hash. Same inputs → same invoice → same hash. This is the basis for the reconciliation ledger.
- **Ingest** (`internal/ingest`) — idempotent event acceptance on `idempotency_key` within a **published, configurable** dedup window; late / out-of-order events are accepted and attributed to their real `event_time`.
- **Store** (`internal/store`) — one interface, two backends: `memory` (tests, demo) and `postgres` (pgx, embedded schema applied on connect). The engine never depends on a concrete DB.
- **HTTP** (`internal/api`) — the `/v1` surface above, no web framework (net/http 1.22+ routing → single binary). Production-hardened: read/write/idle timeouts (slowloris-proof), graceful shutdown that drains in-flight requests on deploy, panic recovery so one bad handler can't take billing down, and a request-body cap. API-key auth gates `/v1` and `/mcp`; optional per-key rate limiting on the ingest hot path.
- **Reconcile** (`internal/reconcile`) — pure diff of stored ledger vs. live recompute; the proof behind `/v1/reconcile`.
- **Payments** (`internal/payments`) — processor-agnostic rail: a `Processor` interface, thin clients for each supported rail (exact minor units, idempotency), a `fake` test double, and a `providers` registry that selects the rail from the environment.
- **Alerts** (`internal/alerts`) — pure 50/80/100% threshold logic + webhook notifier; evaluated on every ingest.
- **Webhooks** (`internal/webhook`) — signed (HMAC-SHA256, `X-Smolbill-Signature`) outbound delivery of lifecycle events; fired on finalize and on every drift the reconciler catches, best-effort so a slow endpoint never blocks billing.
- **Dunning** (`internal/dunning`) — the pure failed-payment-recovery state machine: a configurable retry `Schedule` (default +2h/+1d/+3d/+5d/+7d, grounded in published failed-payment research) and decline-reason routing (`Classify`) so hard declines and authentication challenges stop instead of retrying. No clock of its own — every transition is unit-tested.
- **Comms** (`internal/comms`) — the branded, escalating dunning copy, rendered per event. Templates are **yours to read, edit, and preview**, and the rendered subject + body rides in every dunning webhook so your ESP/SMS just sends it.
- **Web** (`internal/api/web.go` + `templates/`) — server-rendered dashboard, reconcile view, and embeddable customer portal; HTML embedded via `go:embed` (single binary).
- **Engine** (`internal/engine`) — shared compute + plan-building used by both the REST API and the MCP server, so the two can never disagree.
- **MCP** (`internal/mcp`) — intent-only MCP server (no SDK) over both **stdio** and **Streamable HTTP** (API-key authenticated, with the armed-money safety model); the agent sets rules, never touches money math.
- **Schema** (`migrations/0001_init.sql`) — the full v0 Postgres data model.

## Run it

```sh
go test ./...                  # full suite (also: go test -race ./...)
go run ./cmd/smolbill demo     # end-to-end pipeline walkthrough (no DB)
go run ./cmd/smolbill serve    # the HTTP API
```

The `demo` creates a customer, a token meter, and a plan (flat base + graduated token pricing), ingests usage (including a duplicate key that gets ignored and a mid-period start that prorates the base fee), and prints an exact invoice with its meter→line trace and a determinism check.

Postgres integration tests run when `SMOLBILL_TEST_DATABASE_URL` is set; otherwise they skip so `go test ./...` is green with no DB.

## Design principles (non-negotiable)

1. **AI = intent, code = math.** The agent never calculates money, and never moves it unsupervised.
2. **Event-sourced.** State is a replayable log; the invoice is always derivable from raw events.
3. **Idempotent everything.** Every ingest needs a key; the dedup window is documented.
4. **Fail safe = under-bill, never over-bill.**
5. **Reconciliation is the product**, not an afterthought.
6. **One binary, one Postgres.** The simplicity is the differentiator.

## License

**The engine is AGPLv3. The client SDKs are Apache-2.0. A commercial license is
available** for anyone who can't take the AGPL — see [COMMERCIAL.md](COMMERCIAL.md).

- **Engine** (`cmd/`, `internal/`) — **AGPLv3**. Real OSI open source: self-host,
  read, fork, ship. The one obligation: if you modify it and offer it over a network,
  or distribute it, you release your source too — *or* buy a commercial license.
- **Client SDKs** (`sdk/python`, `sdk/typescript`, `sdk/mcp`) — **Apache-2.0**, so
  your app can depend on the smolbill client with no copyleft obligation.
- **Commercial license** — a non-AGPL grant to embed smolbill in a closed-source
  product or hosted service.

### What's open vs Pro

| | Open core (AGPLv3, free) | Pro / Cloud (paid) |
|---|---|---|
| Engine, deterministic invoicing, **reconciliation proof** | ✅ | |
| MCP (stdio + HTTP) + REST + SDKs | ✅ | |
| All payment-rail adapters | ✅ | |
| Dashboard + customer portal + transparent dunning | ✅ | |
| Revenue analytics, SSO/RBAC, audit retention | | ✅ |
| Managed hosting, hosted MCP at scale, SLA | | ✅ |

The open core is genuinely complete — correctness, reconciliation, dunning, the
portal, and every payment rail are free and self-hostable. Pro is managed hosting
and the things that only make sense as a service. Contributions welcome — see
[CONTRIBUTING.md](CONTRIBUTING.md).
