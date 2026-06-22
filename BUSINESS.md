# Business model

How smolbill makes money without ever losing money on the open source.

## The shape (Supabase / Palmier style)

| Layer | What | Cost to us | Price |
|---|---|---|---|
| **Open core** | The whole engine, Apache-2.0, self-hosted | **$0** (runs on the user's box) | free forever |
| **Cloud — Pro** | We run it: managed Postgres, backups, the dunning cron, dashboards | ~$5–10/mo per tenant (single Go binary + small PG) | **flat fee + usage**, margin-positive from customer #1 |
| **Cloud — Scale** | ML retry timing, managed card-updater, SSO/RBAC, revenue analytics, SLA | incremental | higher flat, talk to us |

**Why this can't bleed us:** self-host costs us nothing, so the open-source half is
structurally incapable of losing money. The only way OSS infra loses money is a
*subsidized* free cloud tier, so we don't run a fat one. Every paid plan is priced
above our per-tenant cost. No VC needed to survive.

## How the Cloud is billed: Dodo + dogfooding

- **Payments + tax via [Dodo Payments](https://dodopayments.com) (Merchant of Record).**
  Dodo handles global card acceptance, sales tax / VAT, and compliance for *our*
  billing of Pro customers. This does **not** contradict "smolbill is never a MoR" —
  that promise is about *our customers'* billing. For our own revenue, Dodo is the
  MoR; smolbill stays the engine.
- **We dogfood smolbill to bill the Cloud.** smolbill meters our customers' usage and
  computes their invoice; Dodo collects it. The best demo of the product is that it
  runs our own billing. If it's correct enough for our money, it's correct enough for
  yours.
- **Pricing = flat fee + usage.** A flat monthly base covers the managed instance;
  usage (metered on events, by smolbill itself) scales with how much they bill
  through us. **Never a percent of their revenue** — that's the wedge against Stripe
  Billing's 0.7%, and we won't undercut our own pitch.

## AI-first by design

smolbill is meant to be *driven by AI*. The MCP server exposes the full lifecycle —
create customers, meters, plans, subscriptions; finalize invoices; reconcile; run
the sandbox; read analytics — as intent-only tools (there is no `charge()` tool; the
deterministic engine does every cent). The intended path is **connect your agent
(Claude/Cursor) and get it done by talking.** For purists, every one of those tools
has a REST endpoint and an SDK call too. But the magic, and the reason to pick us, is
that you can run your whole billing setup through AI without touching the math.

## The path to revenue (honest)

1. **Launch** (HN/PH/X) → adoption of the free self-host core. This is the funnel; it
   earns $0 and that's fine.
2. **Stand up the Cloud buy button** (a Dodo payment link for "managed smolbill,
   flat + usage") so inbound has something to buy.
3. **Concierge the first handful of Cloud customers** by hand. First real dollars come
   in weeks-if-we-launch, not from the pricing page existing.
4. **Land-and-expand:** the solo AI builder we win today is the company that grows
   into Scale tomorrow, billed by us the whole way up. We ride customers up-market
   instead of attacking enterprise head-on.

## What's deliberately NOT free (the honest boundary)

Two Cloud-only things genuinely can't be self-hosted, so charging for them doesn't
betray "the engine is free":
- **Cross-merchant ML retry timing** — needs many merchants' data; one self-hoster
  can't replicate it. We ship sensible heuristics free and keep the scheduler
  pluggable so an aggregate model can drop in.
- **Managed card-account-updater / network tokens** — gated by Visa/Mastercard
  enrollment, not code. We expose the integration points; the network service is ours
  to operate.

## Next build for this model

A **Dodo processor adapter** (`internal/payments/dodo`) implementing the existing
`payments.Processor` interface — the same three methods the Stripe adapter has
(`PushInvoice`, `FetchInvoice`, `ChargeInvoice`). Because the core is
processor-agnostic, this is a contained addition, and it's what lets us dogfood
smolbill-on-Dodo to bill the Cloud. (Needs Dodo API keys + a sandbox to build and
verify against, same as the Stripe adapter was built against Stripe's test mode.)
