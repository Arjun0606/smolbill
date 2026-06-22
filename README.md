# meterproof

**The open-source usage-billing engine where your meter and your invoice provably never disagree.**

Single binary, Postgres-only, flat-priced, on top of Stripe, never a Merchant of Record. Apache-2.0.

> **The loop:** AI sets the rule → deterministic code does the math → a reconciliation ledger proves it never drifted.
>
> The AI passes *intent* (`attach_plan`), never money (`calculate_and_charge` is forbidden). A hallucinated decimal in billing ends a business relationship, so the AI never touches the math.

---

## Status: Phase 1 — the deterministic core (done)

This repo currently implements the provably-correct math layer end-to-end, fully unit-tested, Postgres-free at the test boundary (the math is pure functions over plain types). No HTTP, Stripe, or MCP yet — those are Phases 3 and 5.

What's working:

- **Money** (`internal/money`) — exact decimal math, no floats; rounds **down** to the currency minor unit so we always under-bill rather than over-bill (the fail-safe rule).
- **Meters** (`internal/meter`) — `count | sum | max | unique` aggregation over a half-open `[start, end)` period, so adjacent periods never double-count a boundary event.
- **Pricing** (`internal/pricing`) — `flat | per_unit | tiered_graduated | tiered_volume`, with strict tier-ladder validation (malformed plans fail loudly, never silently mis-rate).
- **Invoice** (`internal/invoice`) — deterministic invoice assembly with time-exact proration of flat fees (usage is never prorated), plus a per-line meter trace and a SHA-256 verification hash. Same inputs → same invoice → same hash. This is the basis for the reconciliation ledger.
- **Ingest** (`internal/ingest`) — idempotent event acceptance on `idempotency_key` within a **published, configurable** dedup window; late / out-of-order events are accepted and attributed to their real `event_time`.
- **Store** (`internal/store/memory`) — in-memory storage satisfying the ingest interface; the Postgres impl will satisfy the same shape.
- **Schema** (`migrations/0001_init.sql`) — the full v0 Postgres data model from the build plan §8.

## Run the demo

```sh
go test ./...                 # full suite (also: go test -race ./...)
go run ./cmd/meterproof demo  # end-to-end pipeline walkthrough
```

The demo creates a customer, a token meter, and a Pro plan ($49 base + graduated token pricing), ingests usage (including a duplicate key that gets ignored and a mid-period start that prorates the base fee), and prints an exact invoice with its meter→line trace and a determinism check.

## Design principles (non-negotiable)

1. **AI = intent, code = math.** The agent never calculates money.
2. **Event-sourced.** State is a replayable log; the invoice is always derivable from raw events.
3. **Idempotent everything.** Every ingest needs a key; the dedup window is documented.
4. **Fail safe = under-bill, never over-bill.**
5. **Reconciliation is the product**, not an afterthought.
6. **Postgres-only, single binary.** The simplicity is the differentiator.

## Roadmap

- **Phase 1 — deterministic core** ✅ (this)
- **Phase 2 — reconciliation ledger + entitlements** (`GET /reconcile/{invoice}`)
- **Phase 3 — Stripe push + spend alerts (50/80/100%)**
- **Phase 4 — dashboard + free customer wallet/portal**
- **Phase 5 — MCP server (thin, intent-only)**
- **Phase 6 — single-binary release + Show HN**

## License

Apache-2.0 (intentionally not AGPL).
