# Show HN draft

Draft for launch. Written to sound like me, not a press release: lowercase-leaning,
plain, no em dashes, no hype words. Edit before posting. Post mid-week, early US
morning (HN is busiest ~8-10am ET on Tue/Wed/Thu).

---

## Title

Pick one (HN title max is 80 chars, so the long version gets trimmed; the short one is safer):

- **Show HN: smolbill, open-source usage billing on Stripe (single binary, Postgres-only)**
- Show HN: Open-source usage billing where your meter and invoice can't disagree

Keep "Show HN:" at the front. No emoji, no exclamation marks.

## URL

Point it at the GitHub repo (https://github.com/Arjun0606/smolbill), not a landing page.
HN trusts a repo more than a marketing site, and the README is the pitch.

---

## Post text (the box under the title)

i've been building smolbill, an open-source usage-based billing engine. it sits on
top of stripe (invoicing only, it never holds your money and never becomes a
merchant of record), it's a single static binary with postgres and nothing else,
and it's apache-2.0, not agpl.

the one thing i actually care about: your meter and your invoice can't silently
disagree. every event you send is idempotent and the dedup window is documented.
at invoice time it records a reconciliation ledger (raw events -> meter value ->
invoice line, plus a hash). you can hit GET /reconcile/{invoice} any time and it
recomputes from the live event log and tells you, line by line, if anything
drifted. if a late or out-of-order event showed up after you finalized, it shows
exactly what moved and by how much instead of quietly being wrong.

the money math is all integer-precise decimals, never floats, and it rounds down
so the bias is always toward under-billing rather than charging someone a cent they
didn't owe.

there's also an mcp server so you can set up meters and plans by talking to an
agent (claude, cursor). the agent only ever passes intent (create_plan,
attach_plan). there is no charge() or calculate_bill() tool. the deterministic
engine does every cent. a hallucinated decimal in billing ends a business
relationship, so the model never touches the arithmetic.

the part i'm most happy with: you can simulate a pricing change against your real
usage before you commit it. POST /v1/invoices/simulate (or ask the agent) replays
this period's actual events against a proposed plan and shows you, line by line,
what the bill would have been vs what it is now. it writes nothing. it runs
through the exact same engine that finalizes real invoices, so the simulation
can't disagree with what an actual switch would do. it's the thing i wished
existed every time i was scared to touch pricing.

what it does NOT do (on purpose): no merchant-of-record, no tax, no asc 606
rev-rec, no kafka/clickhouse, no enterprise sso. the simplicity is the point.

it's early and i'd genuinely like to be told where the billing logic is wrong.
the reconciliation ledger exists because billing bugs are unforgiving, so if you
can make the meter and the invoice disagree i want to know.

repo: https://github.com/Arjun0606/smolbill

---

## First comment (post this yourself right after, adds the technical depth)

a few implementation details for anyone curious:

- one binary, `docker run` + postgres, or `go run ./cmd/smolbill serve`. no other
  infra. openmeter users keep asking for exactly this on github (#4414/#4415) and i
  wanted it too.
- pricing models: flat, per_unit, tiered_graduated, tiered_volume. malformed tier
  ladders fail loudly at plan creation instead of silently mis-rating later.
- idempotency is on every event ingest with a published, configurable window.
  late/out-of-order events are accepted and attributed to their real event_time,
  so re-rating a closed period stays correct.
- the reconcile endpoint returns 200 when consistent and 409 with a diff when it
  drifted, so you can alert on it.
- the customer wallet/portal is in the free core. one less thing behind a paywall.
- it's deliberately not a merchant of record. a processor freeze can't take down
  your billing logic or feature gates, because they live in your postgres, not in
  stripe.

stack is go, postgres, shopspring/decimal, pgx. that's the whole dependency list
worth mentioning.

---

## The hero demo (have this ready to paste if asked "show me")

finalize an invoice, then a late event arrives, then reconcile catches it:

```
POST /v1/invoices/finalize {subscription_id}   -> total $3.00, hash 4863...
GET  /v1/reconcile/{id}                         -> 200 {"verdict":"consistent"}

# a late usage event lands after finalize
POST /v1/events {idempotency_key:"late", n:5000}

GET  /v1/reconcile/{id}                         -> 409
{
  "verdict": "drift_detected",
  "stored_total": "3.00", "live_total": "8.00",
  "hash_match": false,
  "diffs": ["invoice total 3.00 -> 8.00 (5.00 drift)"],
  "lines": [{"meter_code":"tokens",
    "diffs":["raw event count 1 -> 2 (+1, likely late/out-of-order events)",
             "meter value 3000 -> 8000","amount 3.00 -> 8.00"]}]
}
```

---

## Comment prep (answers to have ready, don't pre-post these)

- "why not just stripe billing?" stripe caps line items, makes you reintegrate to
  change a metric, and takes 0.7% even off-stripe. i'm flat-priced and the meter
  reconciles with the invoice provably. i also run off any processor, stripe never
  will help you bill off stripe.
- "stripe has an mcp server too, why won't they crush you?" the mcp part isn't the
  moat, it's table stakes (about a dozen billers have one). the durable stuff is
  the business model (no % of your revenue, which is stripe's own revenue line so
  they can't drop it) and independence (cross-provider). the experience gets you
  loved, the structure is why the giant can't take it back.
- "agpl?" no, apache-2.0 on purpose. agpl scares legal teams, that's the lane i want.
- "how is this different from lago/openmeter?" lago is agpl and heavy to self-host
  and the portal is a paid add-on. openmeter wants kafka/clickhouse. mine is one
  binary + postgres, permissive license, portal free.
- "is the finance-can-do-it-without-an-engineer claim real?" no, and i won't
  pretend. config is no-code, but ingestion is ~10 lines of sdk. being honest about
  that.
- "production ready?" no. it's early. preview/reconcile (read paths) are solid, the
  write path to stripe is built but i'd treat it as beta. fail-safe is under-bill.

## Things to do before posting

- [ ] create the github repo, push, confirm README renders
- [ ] tag v0.1.0 so the release workflow publishes binaries (so "single binary" is
      literally true on the releases page, not just a claim)
- [ ] make sure CI is green on the repo (badge in README optional)
- [ ] be free for ~6 hours after posting to answer every comment. that's the whole
      game on launch day.
