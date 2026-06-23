# Business model

How smolbill makes money without ever losing money on the open source — and without
becoming a free tool nobody pays for.

## The shape (AGPL open-core + closed Pro)

| Layer | What | License | Cost to us | Price |
|---|---|---|---|---|
| **Open engine** | The whole billing engine: metering, deterministic invoicing, reconciliation, dunning logic, MCP + REST, dashboard, customer portal, Stripe + all processor adapters | **AGPLv3** | **$0** (runs on the user's box) | free forever |
| **Client SDKs** | Python / TypeScript / MCP clients | **Apache-2.0** | $0 | free forever |
| **Pro (closed)** | Revenue analytics, SSO/RBAC, cross-merchant ML retry timing, card-account-updater / network tokens, hosted MCP HTTP at scale, audit-log retention, SLA | proprietary | incremental | flat + usage / annual |
| **Commercial license** | Use the engine in a closed-source product or hosted service **without** AGPL obligations | proprietary | $0 | per-company annual |
| **Cloud (managed)** | We run it: managed Postgres, backups, the dunning cron, dashboards | proprietary ops | ~$5–10/mo per tenant | flat + usage, margin-positive from customer #1 |

**Two independent revenue lines, not one:** (1) people who want it *run for them*
(Cloud) and (2) people who can't take the AGPL or want the closed Pro features
(commercial license). Either can stand alone.

## Why this can't bleed us

- **Self-host costs us nothing**, so the open half is structurally incapable of
  losing money. The only way OSS infra loses money is a *subsidized free cloud tier* —
  we don't run a fat one. The free tier is self-host.
- **Every paid plan is priced above our per-tenant cost.** No VC needed to survive.

## Why it won't be "a free tool nobody pays for" (the fix)

The earlier plan gave the whole engine away under Apache-2.0 and only charged for
hosting. That's the trap: Apache lets anyone fork it closed and resell against us,
and "we host a single Go binary" is the *weakest* possible moat. Two deliberate
changes close it:

1. **AGPLv3 on the engine, not Apache.** Real OSI open source (keeps the HN/PH cred),
   but it closes the SaaS loophole: anyone who modifies smolbill and offers it over a
   network, distributes it, or embeds it in a closed-source product must release their
   source — *or* buy a **commercial license**. Crucially, **most enterprises have a
   blanket no-AGPL policy**, so for them the commercial license is the only way to use
   it at all. That scared-legal-team reaction isn't a bug — it's the conversion event.
   (We keep the **client SDKs Apache-2.0** so apps can integrate freely; the copyleft
   is on the engine, exactly like MongoDB's server-vs-drivers split.)
2. **A real *closed* Pro, not just hosting.** The trust-builders stay free (engine,
   reconciliation, MCP, Stripe, dashboard, portal). The high-value / ops / network
   features are **closed source and paid** — they aren't in the AGPL repo at all.
   Charging for closed features we built is unimpeachable, and several of them
   (cross-merchant ML, network tokens) *genuinely can't* be self-hosted.

## How the Cloud + Pro are billed: Dodo + dogfooding

- **Payments + tax via [Dodo Payments](https://dodopayments.com) (Merchant of Record).**
  Dodo handles global card acceptance, sales tax / VAT, and compliance for *our*
  billing. This does **not** contradict "smolbill is never a MoR" — that promise is
  about *our customers'* billing. For our own revenue, Dodo is the MoR; smolbill stays
  the engine. (The `internal/payments/dodo` adapter is built and tested.)
- **We dogfood smolbill to bill the Cloud.** smolbill meters our customers' usage and
  computes their invoice; Dodo collects it. The best demo is that it runs our own
  billing: if it's correct enough for our money, it's correct enough for yours.
- **Pricing = flat fee + usage.** A flat base covers the managed instance / license;
  usage scales with how much they bill through us. **Never a percent of their
  revenue** — that's the wedge against Stripe Billing's 0.7%.

## The funnel (free → paid, at the friction points)

The free engine isn't charity — it's the top of the funnel, and the product nudges
toward paid at the exact walls:

1. **Adopt** the free self-host engine (HN/PH/X). It earns $0 and that's the point.
   Provable reconciliation gets them dependent on correctness.
2. **Hit a wall** the product surfaces in-context: add a 2nd processor, want the
   hosted MCP HTTP endpoint, open revenue analytics, need SSO — each is a Pro nudge
   in the dashboard / MCP.
3. **Hit the license wall:** the moment a company wants smolbill in a closed-source
   product or as a service, AGPL forces a decision → self-serve commercial license
   (Dodo checkout). Legal-policy bans convert here too.
4. **Or skip the ops:** don't want to run Postgres + cron + upgrades → Cloud.
5. **Land-and-expand:** the solo AI builder we win today is the company that grows
   into a commercial license + Scale tomorrow, billed by us the whole way up.

No sales emails, no gated evaluation — every upgrade is self-serve (permissionless).

## What's deliberately closed (the honest boundary)

These are paid because they're genuinely uncloneable or genuinely cost us to run —
not because we crippled the open core:

- **Cross-merchant ML retry timing** — needs many merchants' data; one self-hoster
  can't replicate it. The open core ships sensible heuristics and a pluggable
  scheduler so the aggregate model can drop in.
- **Managed card-account-updater / network tokens** — gated by Visa/Mastercard
  enrollment, not code. We expose the integration points; the network service is ours.
- **Revenue analytics, SSO/RBAC, audit retention, hosted MCP at scale, SLA** — the
  enterprise/ops surface. Closed, billed, never pretended to be in the OSS repo.

## The honest read on the number

The structure can reach **$100k MRR** — roughly 500 accounts at ~$200/mo, or ~80
commercial licenses at ~$1.2k/mo, or a blend. But billing infra is **trust-gated and
slow to adopt** (nobody swaps billing casually). The AGPL + closed-Pro model is what
makes the number *possible*; the binding constraint is **distribution and time**, not
the model. Treat $100k as a real 12–24 month target, not a quick one.
