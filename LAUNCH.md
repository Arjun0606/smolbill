# Launch playbook

The HN post lives in [SHOW_HN.md](SHOW_HN.md). This is everything else: Product
Hunt, X, the sequencing, and how the spike turns into a community that compounds.

**The positioning (everything keys off this one line):**

> The open-source usage-billing engine for AI apps and solo devs. Flat-priced,
> never a percent of your revenue. Single binary, provably correct, not built for
> a 50-person finance team.

We are not first to usage billing. We are first to *that bundle* — open-source +
AI-native + flat-priced + dead-simple + provably correct. Own the solo/AI-builder
beachhead so hard they evangelize it, then ride those customers up-market as they
grow. Land and expand, not boil the ocean.

---

## The funnel (important: HN and PH are spikes, not homes)

You get roughly one good Show HN and one good PH launch per product. They are
one-day *acquisition spikes*, not places a community lives. The community
compounds where you own the relationship:

```
HN / PH launch day  ──spike──▶  GitHub repo + the landing page
                                        │
                                        ▼
                            X (build in public, daily)  +  Discord / GitHub Discussions
                                        │
                                        ▼
                              first 50 users who can't shut up about it
```

So before launch day, two things must exist for the spike to stick:
1. a **Discord invite** (or GitHub Discussions enabled) linked in the README + landing page
2. an **X account** posting build-in-public, so people who miss the spike still find a live thread

---

## Product Hunt

**Tagline (≤60 chars):**
- Open-source usage billing for AI apps and solo devs

**Description (≤260 chars):**
> Meter usage, send a correct invoice, never give up a % of your revenue. A single
> Go binary on top of Stripe, Apache-2.0. Reconciliation ledger proves your meter
> and invoice never silently disagree. Decline-aware dunning + TS/Python SDKs free.

**Topics:** Developer Tools, SaaS, Artificial Intelligence, Open Source, Payments

**First comment (the maker's note — post it the second you go live):**
> hey PH. i'm a solo dev. every usage-billing tool i tried was either a cut of my
> revenue (stripe billing = 0.7%) or built for a finance team i don't have (lago,
> orb, zuora). so i built smolbill: open source, a single go binary + postgres, and
> flat-priced — never a percent of what you make.
>
> the part i actually care about: it keeps a reconciliation ledger, so it can prove
> your meter and your invoice never silently disagree, or show you exactly where
> they drifted. billing bugs that quietly overcharge people are the nightmare, and
> this is the fix.
>
> it also does decline-aware dunning (free — the thing lago/chargebee paywall),
> ships TS + python SDKs, and has an MCP server so your agent sets up billing by
> passing intent, never doing the math. self-host is free forever. would love your
> feedback, especially on the correctness model.

**Gallery shot list (5 images, build them from the dashboard + quickstart):**
1. the one-liner on a clean background (the positioning)
2. `go run ./cmd/smolbill quickstart` terminal → the guided tour links
3. the reconciliation ledger catching drift (the 409 + line-level diff)
4. the dashboard / customer portal
5. the pricing line: "self-host free · cloud flat-priced · never a % of your revenue"

**Timing:** PH resets at 12:01am PT. Post right at 00:01 PT to get a full day on the
board. Line up a handful of people who'll genuinely try it and comment in the first
few hours (no fake upvotes — PH detects rings and it's not worth it).

---

## X (build in public, this is the home base)

**Launch thread (your voice — lowercase, no em dashes, no hype):**

1/
> every usage billing tool is either a cut of your revenue or built for a 50 person
> finance team. i'm a solo dev shipping an ai app, i just wanted to meter usage and
> send a correct invoice. so i built smolbill. open source, single binary, flat
> priced, never a % of your revenue.

2/
> the one thing i actually care about: it can prove your meter and your invoice
> never silently disagree. it keeps a reconciliation ledger and recomputes the bill
> from your raw events on demand. if a late event drifted the total it shows you the
> exact line and amount instead of quietly being wrong.

3/
> it does decline-aware dunning for free (the thing lago and chargebee paywall). it
> retries failed payments but stops hammering a dead card, because that just trips
> the bank's fraud limits. routes by decline reason.

4/
> ai native: your agent sets up billing by passing intent (create_meter, attach_plan).
> there is no charge() tool. the deterministic engine does every cent. a hallucinated
> decimal in billing ends a business relationship so the model never touches the math.

5/
> apache-2.0, single go binary + postgres, ts + python sdks. self host free forever.
> see the whole thing run in 60 seconds: `go run ./cmd/smolbill quickstart`
> repo + discord in the replies. tear it apart, i want to know where the billing
> logic is wrong.

**After launch:** post one build-in-public update most days. what you shipped, a
real number (stars, first user, a bug someone found). small and honest beats a big
relaunch. never auto-DM or mass-reply strangers — paced and human, your voice.

---

## Sequencing on launch day (pick a Tue/Wed/Thu)

| time (ET) | action |
|---|---|
| 00:01 PT (3:01am ET) | Product Hunt goes live + maker's first comment |
| ~8:00am ET | Show HN goes live (HN peaks ~8–10am ET) + your first comment right after |
| ~8:15am ET | X launch thread, link the repo + Discord (not the HN/PH links directly) |
| all day | sit and answer every single comment on HN, PH, X. this is the whole game. |

Do NOT split into separate days — one coordinated push concentrates the signal.

---

## The objection that decides it: "why not just Stripe Billing / Lago?"

It will be the top comment. Answer calmly, it's already true:
- **Stripe Billing** takes 0.7% of your revenue even off-Stripe, caps line items, and
  makes you reintegrate to change a metric. smolbill is flat-priced and runs on any
  processor.
- **Lago** is AGPL (scares legal teams), heavier to self-host, and gates auto-dunning
  + the portal behind premium. smolbill is Apache-2.0, one binary, those are free.
- **Orb/Metronome/Zuora** are excellent and built for companies with a billing team.
  smolbill is for the solo dev and the two-person AI startup who just want it to work.
- **The honest line:** config is no-code, but ingestion is ~10 lines of SDK. it's
  early. read paths (preview/reconcile/simulate) are solid; the Stripe write path is
  beta. fail-safe is always under-bill. don't oversell it — HN rewards honesty.

---

## Pre-launch checklist

- [ ] GitHub repo public, README renders, CI green
- [ ] tag v0.1.0 so the release page has real binaries ("single binary" provable)
- [ ] Discord created + invite in README and landing page
- [ ] landing page live (see `site/`) with the GitHub + quickstart + cloud-beta CTAs
- [ ] the cloud "buy" path exists, even if it's just a Stripe Payment Link + waitlist
- [ ] you are genuinely free for ~8 hours on launch day to answer everything
