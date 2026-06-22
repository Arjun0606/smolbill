# smolbill — complete UI design brief (for Stitch)

The full product, not a demo: ~75 screens across marketing, auth, onboarding, the
app, settings, the embeddable customer portal, and system states.

**How to use:** paste **Product**, **Visual identity**, and **Shared patterns** once to
set context, then paste any screen's block to generate it. Screens marked ★ are the
soul of the product — do those first.

---

## Product (context)

smolbill is an open-source, developer-first **usage-based billing** platform for AI
apps and solo developers. You meter usage (tokens, API calls, anything), turn it into
correct invoices, and recover failed payments — without giving up a percent of your
revenue or needing a finance team. Its signature is **provable correctness** (a
reconciliation ledger proves your meter and your invoice never silently disagree) and
being **AI-native**: you set up and run your whole billing by talking to an AI agent.
Audience: technical founders and indie devs. The polish of Linear, Vercel, Stripe,
Supabase.

## Visual identity (dark + warm amber — simple, bold, cool)

Inspiration: a premium dark editorial/newsletter look — deep black, crisp white
headlines, and a single warm honey-amber accent doing all the work.

- **Mood:** dark, confident, editorial, effortless. Bold and minimal; one warm accent
  against deep black. Cool and modern, never corporate or busy.
- **Theme:** near-black background (#0A0A0A), cards a touch lighter (#161616), subtle
  1px borders (#262626). Headlines crisp white (#FAFAFA), body muted gray (#8E8E8E).
  Lots of black space.
- **Accent — one warm amber / honey-gold (#F5A623):** the *only* accent. Use it for the
  key word in a headline, primary buttons (amber fill + near-black text), active nav,
  links and "→" CTAs, and key/positive numbers. Used sparingly = it pops.
- **Status mapping (keep it simple):** amber = active / paid / healthy / consistent /
  the highlight; a muted warm red (#E5544B) ONLY for the hard-negative (drift / failed /
  uncollectible / over-limit); gray = draft / inactive. No other colors.
- **Type:** a heavy, slightly tight **bold sans** for headlines (think Inter Display
  Black) — big and confident, with the key word in amber; regular weight for body;
  **monospace** (JetBrains Mono) for money, IDs (cus_…/inv_…), hashes, meter codes.
- **Components:** dark **rounded-14px** cards with hairline borders (no heavy shadows on
  black), small uppercase gray category/status pills, airy data-dense tables, amber
  primary buttons, copyable mono IDs.
- **The "pop":** amber on black is the whole identity — one warm color, lots of dark,
  bold type. Simple and cool.

> **Remap note for the screens below:** wherever a screen says "green," use the **amber**
> accent. Positive / consistent / paid / recovered = **amber**; hard-negative drift /
> failed / uncollectible / over-limit = **muted red**; at-risk / pending = amber (dim).
> Everything else is black + white + gray.

## Shared patterns (apply to every app screen)

- **App shell:** slim left sidebar — workspace switcher at top; nav: Overview,
  Customers, Subscriptions, Invoices, Plans & Meters, Usage, Dunning, Analytics,
  Developers, Settings; a green "● AI connected" status and the user avatar at the
  bottom. Top bar: breadcrumb, a ⌘K search, a bell (notifications), a green "+ New".
- **Tables:** comfortable rows, mono for IDs/amounts, status pills, row hover, a
  right-aligned "⋯" action menu, pagination, column sort, a filter bar.
- **Detail pages:** header with title + copyable mono id + status pill + actions;
  tabs below; content in cards.
- **Create/edit:** a right-side **drawer** with a form, primary green button.
- **Every list has:** an empty state (icon + one line + a green CTA), a loading
  skeleton, and an error state.

---

# A. Marketing / public

**A1. Landing / home ★** — Dark hero: headline *"Usage billing for AI apps and solo
devs. Never a percent of your revenue."* subhead, a green "Star on GitHub" + ghost
"Get the hosted beta." A terminal code block (`go run ./cmd/smolbill quickstart` + a
3-line SDK snippet). A comparison strip (Stripe 0.7% · Lago/Orb/Zuora need a finance
team · smolbill flat + open source). A 4-card "why" grid (never a %, provably correct,
AI-native, free dunning). Logos/social proof row. Footer.

**A2. Pricing** — 3 tiers (Self-host $0 · Cloud flat+usage · Scale "talk to us") as
cards, the middle one green-bordered "popular." A feature comparison table below. An
FAQ accordion. CTA band.

**A3. Docs home** — left doc-nav tree, a search, a getting-started grid of cards
(Quickstart, SDKs, MCP, Reconciliation, Dunning), right-side "on this page" TOC.

**A4. Docs article** — the doc-nav + a long-form article with code blocks (copy
buttons), callout boxes, and prev/next links.

**A5. Changelog** — a vertical timeline of dated releases with tags (feature/fix),
each entry a short note. Quiet, scannable.

**A6. Status page** — overall "All systems operational" banner (green), a list of
components (API, Dashboard, MCP, Webhooks) each with an uptime bar, and an incident
history list.

**A7. 404 / error (public)** — minimal, on-brand, a mono "404" + one line + a button
home.

**A8. Legal** — clean long-form terms/privacy layout with a section nav.

# B. Auth

**B1. Sign up** — centered card on dark: email + password (or "Continue with GitHub /
Google" buttons first), a green "Create account," a link to sign in. Left side (on
wide screens) a subtle product value-prop panel.

**B2. Sign in** — same card: email + password, "Continue with GitHub/Google," forgot
link, green "Sign in."

**B3. Magic link sent** — a calm "Check your email" card with an envelope icon and a
"resend" link.

**B4. Forgot password** — email field + "Send reset link."

**B5. Reset password** — new password + confirm, strength meter.

**B6. Two-factor verify** — a 6-digit code input (segmented), "verify," a "use a
backup code" link.

**B7. SSO sign-in (enterprise)** — a single "Sign in with your company" / SAML domain
field.

**B8. Accept invite** — "You've been invited to {workspace}" card with the inviter,
role pill, and a green "Accept & join."

**B9. Verify email** — confirmation success/failure state.

# C. Onboarding

**C1. Welcome / create workspace** — "Name your workspace," currency default, a green
"Continue." A progress indicator (step 1 of 4).

**C2. Connect your AI agent ★** — "Set up billing by talking." A copyable one-line MCP
config block (`npx -y smolbill-mcp`), buttons "Open in Claude / Cursor," and a faux
chat showing a user saying *"set up usage billing, $0.001/token + $20 base"* and the
agent confirming it created the meter, plan, and a demo customer (IDs as mono pills).

**C3. Connect a payment processor** — cards for Stripe and Dodo, each with a "Connect"
button; a "skip for now (local-only)" link. Shows connected state with a green check.

**C4. Quickstart your billing** — a simple form mirroring quickstart_billing (product
name, what you meter, unit price, optional base) with a live "this is the plan you'll
get" preview card; or a "do it by talking instead" toggle to C2.

**C5. Invite your team** — email chips + role select, "send invites," "skip."

**C6. Onboarding checklist / you're set** — a checklist card (create workspace ✓,
connect AI ✓, set up billing ✓, connect processor ○, invite team ○) with a green
"Go to dashboard."

# D. Core app

**D1. Dashboard home (overview) ★** — 4 stat cards (Projected revenue this period ·
Active subscriptions · Finalized revenue MTD · At-risk in dunning), each a big mono
number + label + tiny sparkline. Two columns: a **Recent activity** feed (invoice
finalized / payment recovered / drift detected / over-limit, each with a status pill +
timestamp), and a **Recovery** panel (donut of retrying/recovered/written-off + the
recovery-rate %). A slim "set up" checklist if onboarding is incomplete.

**D2. Command palette (⌘K overlay)** — a centered search overlay: fuzzy search across
customers, invoices, plans + quick actions ("Create customer", "Run dunning",
"Preview invoice"), keyboard-navigable, mono ids.

**D3. Notifications panel** — a right-side slide-over: grouped notifications (drift
detected, payment recovered, dunning needs action) with read/unread, "mark all read."

# E. Customers

**E1. Customers list** — table: name, mono `cus_…` (copyable), plan pill, current
projected bill (mono), status, created. Filter bar + search + "+ New customer." Empty
state with a green CTA.

**E2. Customer detail ★** — header (name, copyable id, plan pill); small cards
(current projected bill, wallet balance, status). Tabs: **Usage** (per-meter
breakdown, mono qty + amount, projected total), **Invoices** (their invoice list),
**Entitlements** (each feature with a used/limit usage bar — green within, red over),
**Wallet** (balance + transactions), **Activity** (timeline).

**E3. Create customer (drawer)** — name + optional external id, green "Create."

# F. Subscriptions

**F1. Subscriptions list** — table: customer, plan + version, status pill, current
period, next renewal.

**F2. Subscription detail** — plan + period, a "change plan" action that opens the
**simulate** sandbox (see H4), proration preview, cancel (with a confirm dialog).

# G. Plans, meters, entitlements

**G1. Meters list** — cards/table: meter code (mono), name, aggregation (count/sum/
max/unique), the property it sums.

**G2. Meter create/detail (drawer)** — code, name, aggregation segmented control,
property key.

**G3. Plans list** — cards per plan: name + version tag, the price components (flat
base + per-unit / tiers) in mono, "n subscribers."

**G4. Plan builder (create/edit) ★** — a clean form: name; a price list where each row
is a pricing model (segmented: flat / per-unit / tiered-graduated / tiered-volume),
currency, amount(s); for tiered, an editable tier ladder (up-to + unit price rows). A
live "example bill at X units" preview on the right.

**G5. Entitlements management** — per customer or global: feature name, kind
(boolean/metered), linked meter, limit, with a usage bar for metered ones.

# H. Usage, invoices, sandbox

**H1. Usage explorer / event log** — a searchable, filterable stream of raw usage
events: timestamp, customer, meter, properties (mono JSON), idempotency key. Filter by
customer/meter/time. This is the audit-grade raw log.

**H2. Invoices list** — table: mono `inv_…`, customer, period, total (mono), status
pill (draft/finalized/paid), a reconcile indicator (green check / red drift).

**H3. Invoice + reconciliation detail ★★ (the hero)** — header (mono id, customer,
period, big mono total, status pill). Line items table (meter code, qty, unit price,
amount — all mono). Then the **Reconciliation** panel: a large verdict banner — green
*"Consistent — meter and invoice provably agree"* with a check + the verification hash
in mono, OR red *"Drift detected"* showing line-by-line stored→live (e.g. "raw events
1 → 2", "total 3.00 → 8.00") with deltas highlighted. A secondary "Verify against
processor" button. This screen must feel like proof.

**H4. Preview / simulate (sandbox) ★** — pick a customer, edit a proposed plan, and
see a **side-by-side diff**: current bill vs proposed, per line, with deltas in
green/red and a "nothing is committed" reassurance. The "prove a price change is safe"
screen.

**H5. Reconciliation history / audit log** — a list of every reconcile/verify run with
its verdict + timestamp, filterable, for compliance/audit.

# I. Dunning / recovery

**I1. Dunning dashboard ★** — collections table: customer, amount (mono), status pill
(scheduled/retrying/requires-action/recovered/uncollectible in the right colors),
attempts, next retry time. Top: small cards (at-risk total, recovered MTD, recovery
rate). A "Run dunning now" action.

**I2. Collection detail / retry timeline** — a vertical stepper of each attempt (result
+ decline reason), the upcoming retry dimmed with its scheduled time, and the
decline-routing note (e.g. "hard decline → stopped, awaiting card update").

**I3. Dunning template editor ★** — left: a list of the 4 events
(payment_failed/requires_action/recovered/uncollectible). Selected: a subject field +
a body textarea with insertable field chips ({{.CustomerName}}, {{.Amount}}, …). Right:
a **live email preview** card rendering exactly what the customer sees. "Reset to
default" + a "customized" badge. Emphasize *you own this copy*.

**I4. Recovery analytics** — recovery rate over time, amount at-risk vs recovered, and
recovery-by-decline-reason as a horizontal bar list.

# J. Wallet / credits

**J1. Wallet management** — per customer: balance (big mono), a transaction ledger
(credits/debits, reason, date), a green "Top up" button.

**J2. Top-up (drawer)** — amount + currency + reason, idempotency handled, confirm.

# K. Developers

**K1. API keys** — table of keys (name, masked key, created, last used), "+ Create
key" → a one-time-reveal modal with a copy button + warning.

**K2. Webhooks list** — endpoints (url, subscribed events as pills, status), "+ Add
endpoint."

**K3. Webhook create/edit (drawer)** — url + event multi-select + the signing secret
(revealed once).

**K4. Webhook delivery log ★** — per endpoint: a table of recent deliveries (event
type, response code, timestamp), each expandable to the signed payload (mono JSON) +
headers, with a "resend" button. The thing every dev wishes Stripe did better.

**K5. SDKs & MCP** — a page with copyable install snippets (npm/pip, the MCP `npx`
line) and links to docs.

# L. Analytics

**L1. Analytics overview ★** — a big area chart (projected vs finalized revenue over
time), revenue-by-currency breakdown, top customers by spend, and the dunning recovery
summary. Mono figures, BI-grade but calm — the numbers a founder checks.

# M. Settings

**M1. Account / profile** — name, email, avatar, password, 2FA toggle.

**M2. Workspace / general** — workspace name, default currency, timezone, logo.

**M3. Team & roles (RBAC)** — members table (avatar, email, role select
owner/admin/member, status), pending invites, "+ Invite."

**M4. Payment processor** — connected processor (Stripe/Dodo) with status, keys
(masked), "test connection," switch/disconnect.

**M5. Your smolbill billing ★** — the dogfood screen: the workspace's *own* smolbill
Cloud subscription (plan, this month's usage metered by smolbill itself, the running
flat+usage total), invoice history, and the payment method (via Dodo). Literally
smolbill billing you, shown to you.

**M6. Notifications preferences** — toggles for which events email/notify you.

**M7. Danger zone** — export data, delete workspace (typed-confirm dialog).

# N. Customer portal (embeddable, customer-facing — calm, white-glove, light-friendly)

**N1. Portal — usage & bill ★** — the customer sees their current usage by meter,
projected bill (big mono total), and period. Minimal, trustworthy, a subtle "metered
by smolbill" footer.

**N2. Portal — update payment method ★** — the card-update page the dunning emails link
to. A clean card form (or processor element), reassuring copy ("update to keep your
account active"). Must always work — it's the recovery lynchpin.

**N3. Portal — invoices & history** — their past invoices, downloadable.

**N4. Portal — wallet** — their prepaid balance + transactions.

# O. System states (design these as variants)

**O1. Empty states** — for each list (no customers / no invoices / no webhooks): a
centered icon, one friendly line, a green CTA, and a "or set it up by talking" hint.

**O2. Loading skeletons** — shimmer placeholders matching each layout (cards, tables,
detail headers).

**O3. Error / permission states** — 404 (in-app), 500, "you don't have access," and a
generic "something went wrong — retry," all on-brand.

**O4. Maintenance** — a calm full-page notice.

---

## Build order (suggested)

1. **H3 Invoice+reconciliation**, **C2/Connect-AI**, **I3 template editor** — the soul.
2. **D1 dashboard**, **E2 customer detail**, **I1 dunning**, **L1 analytics** — the spine.
3. **B1/B2 auth**, **C1–C6 onboarding**, **M-section settings** — the frame.
4. **N portal**, **K developers**, **A marketing**, **O states** — the edges.

Keep the shell + identity identical across all of them so it reads as one product.
