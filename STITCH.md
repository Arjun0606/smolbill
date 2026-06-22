# smolbill — UI design brief (for Stitch)

Paste the **Product + Visual identity** blocks first to set context, then paste one
**Screen** block at a time to generate each view.

---

## Product (context)

smolbill is an open-source, developer-first **usage-based billing** platform for AI
apps and solo developers. You meter usage (tokens, API calls, anything), turn it
into correct invoices, and recover failed payments — without giving up a percent of
your revenue or needing a finance team. Its signature is **provable correctness** (a
reconciliation ledger proves your meter and your invoice never silently disagree)
and being **AI-native**: you set up and run your whole billing by talking to an AI
agent. The product should feel precise, trustworthy, and effortless — billing you
can actually understand at a glance.

Audience: technical founders and indie developers. Think the polish of Linear,
Vercel, Stripe, and Supabase dashboards.

## Visual identity

- **Mood:** modern developer tool. Calm, precise, trustworthy, a little "terminal."
  Generous whitespace, nothing cluttered. Confidence through clarity, not decoration.
- **Theme:** dark by default — a near-black charcoal background (#0B0D0C), elevated
  panels in a slightly lighter charcoal (#121514), hairline borders (#1F2421).
- **Accent:** a single vivid green (#54D98C) used sparingly for primary actions,
  positive states, active nav, and key numbers. Never more than one accent.
- **Text:** off-white (#E9EFE9) primary, muted sage-gray (#8A958C) secondary.
- **Status colors:** green = healthy/paid/recovered, amber = retrying/at-risk,
  red = drift/uncollectible/over-limit.
- **Typography:** a clean geometric sans (Inter / Geist) for UI; a **monospace**
  (JetBrains Mono / SF Mono) for all money amounts, IDs (cus_…, inv_…), hashes,
  meter codes, and code snippets. Money and IDs in mono is a core part of the look.
- **Components:** rounded-12px cards, subtle borders over shadows, small mono "pill"
  tags for status, data-dense tables with comfortable row height, copyable IDs.

---

## Screen 1 — Dashboard home (overview)

A billing command center. Top: a row of 4 stat cards — **Projected revenue this
period**, **Active subscriptions**, **Finalized revenue (MTD)**, **At-risk (dunning)**
— each a big mono number with a small label and a tiny trend sparkline. Below, two
columns: left, a **Recent activity** feed (invoices finalized, payments recovered,
drift detected, each with a status pill and timestamp); right, a **Recovery** panel
showing a donut of dunning outcomes (retrying / recovered / written off) and the
recovery-rate percentage. A slim left sidebar nav: Overview, Customers, Invoices,
Plans, Dunning, Analytics, Settings — with a small green "Connected to AI" indicator
at the bottom. Top bar: workspace name, search, and a green "+ New" button.

## Screen 2 — Customer detail

Header: customer name, a copyable mono `cus_…` id, and their plan as a pill. A row
of small cards: **current projected bill**, **wallet balance**, **status**. Tabs:
**Usage**, **Invoices**, **Entitlements**, **Wallet**, **Activity**. The Usage tab
shows a live per-meter breakdown (meter code in mono, quantity, amount) and a
projected total. The Entitlements tab shows each feature with a usage bar (used /
limit) — green within limit, red over. Clean, scannable, data-dense but airy.

## Screen 3 — Invoice + reconciliation (the hero)

The signature screen. Top: invoice header (mono `inv_…`, customer, period, a big
mono **total**, a status pill). The line items in a table: meter code, quantity,
unit price, amount — all mono. Then the **Reconciliation** panel — the proof. A large
verdict banner: green **"Consistent — meter and invoice provably agree"** with a
checkmark and the verification hash in mono, OR a red **"Drift detected"** banner
showing, line by line, stored vs live (e.g. "raw events 1 → 2", "total 3.00 → 8.00")
with the deltas highlighted. This panel is the emotional core: it should feel like
proof, like a receipt you can trust. Include a subtle "Verify against processor"
secondary button.

## Screen 4 — Dunning / recovery

A table of collections (invoices in recovery): customer, amount (mono), a status pill
(scheduled / retrying / requires action / recovered / uncollectible with the right
status color), attempts, and **next retry** time. Click a row to expand a **retry
timeline** — a vertical stepper showing each attempt, its result, and the decline
reason, with the upcoming retry dimmed. A side panel: the **message template editor**
— a subject field, a body textarea with `{{.CustomerName}}` style chips, and a **live
preview** card beside it rendering the email exactly as the customer will see it.
Emphasize "you own this copy" — it's editable and previewable.

## Screen 5 — Connect AI (the setup magic)

The "extremely easy" screen. Centered, minimal. A headline: **"Set up billing by
talking."** A code block showing the one-line MCP config (`npx -y smolbill-mcp`) with
a copy button. Below it, a faux chat exchange illustrating the magic: a user bubble
*"set up usage billing for my AI app, $0.001 a token with a $20 base"* and an
assistant bubble confirming it created the meter, plan, and a demo customer, with the
resulting IDs as mono pills. A green "Open in Claude / Cursor" button. Feels like the
future — billing configured in a sentence.

## Screen 6 — Plans & meters (pricing config)

Two sections. **Meters:** cards showing each meter (code in mono, aggregation type,
the property it sums). **Plans:** cards for each plan showing the price components
(flat base + per-unit, or tiers) in a clean mono layout, with a version tag. A green
"+ Create plan" button opens a side drawer with a simple form (name, prices, pricing
model as a segmented control: flat / per-unit / tiered). Calm and structured.

## Screen 7 — Analytics

Revenue and recovery over time. A big area chart of revenue (projected vs finalized),
a breakdown of revenue by currency, and a **dunning recovery** section: recovery rate,
amount at risk vs recovered, and recovery-by-decline-reason as a horizontal bar list.
All figures mono. Clean, BI-grade but not busy — the four numbers a founder checks.

## Screen 8 — Landing page (marketing)

Hero on the dark theme: headline **"Usage billing for AI apps and solo devs. Never a
percent of your revenue."** Subhead about metering usage and sending correct invoices
with no sales call. Two buttons: a green "Star on GitHub" and a ghost "Get the hosted
beta." Below the hero, a terminal-style code block (`go run ./cmd/smolbill
quickstart` and a 3-line SDK snippet). Then a comparison strip ("Stripe Billing takes
0.7% · Lago/Orb/Zuora need a finance team · smolbill is flat-priced and open source")
and a 4-card "why" grid (never a %, provably correct, AI-native, free dunning). A
simple 3-tier pricing section (Self-host $0 / Cloud flat+usage / Scale). Confident,
spacious, developer-credible.

## Screen 9 — Customer portal (embeddable)

A clean, minimal view a customer sees: their current usage by meter, projected bill
(big mono total), wallet balance, and entitlement limits with usage bars. A subtle
"metered by smolbill" footer. Light, simple, trustworthy — this is shown to *their*
customers, so it should feel calm and white-glove.
