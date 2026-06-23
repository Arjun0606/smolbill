# Deploying smolbill

One static Go binary + Postgres. No Kafka, no ClickHouse, no migrate step — the
Postgres schema self-applies on first connect. Pick a path:

## Local (Docker Compose)

```
docker compose up --build
# engine on http://localhost:8080  ·  Postgres alongside it
open http://localhost:8080/dashboard
```

## Fly.io

```
fly apps create smolbill
fly postgres create                 # managed PG
fly postgres attach <pg-app-name>   # sets DATABASE_URL secret automatically
fly secrets set SMOLBILL_CLOUD_MODE=waitlist SMOLBILL_CLOUD_URL=https://...   # funnel
fly deploy                          # uses fly.toml + Dockerfile
```

Health check (`GET /healthz`) and HTTPS are configured in `fly.toml`.

## Render.com

Push the repo, **New → Blueprint**, point at `render.yaml`. It provisions the
web service (Docker) + managed Postgres and wires `DATABASE_URL`. Set the secret
env vars (processor keys, `SMOLBILL_CLOUD_URL`) in the dashboard.

## Environment

| Var | Purpose |
|---|---|
| `DATABASE_URL` | Postgres DSN. Unset → in-memory store (dev only, not persisted). |
| `ADDR` | HTTP listen address (default `:8080`). |
| `SMOLBILL_API_KEYS` | Comma-separated API keys. **Set this in production** — when present, every `/v1` request needs `Authorization: Bearer <key>` or `X-API-Key: <key>`. Unset → `/v1` is open (dev only). `/healthz`, the dashboard, and marketing pages stay open. |
| `SMOLBILL_RATE_LIMIT` | Optional. Events/sec cap on `POST /v1/events`, per key (or IP). Unset → no limit. |
| `SMOLBILL_DUNNING_INTERVAL` | Optional. Runs the failed-payment retry sweep on this cadence, e.g. `1h`. Needs a payment rail. Unset → no scheduler (trigger `POST /v1/dunning/run` yourself). |
| `SMOLBILL_PROCESSOR` | Payment rail: `stripe\|dodo\|paddle\|lemonsqueezy\|polar\|creem\|razorpay\|crypto`. Unset → auto-detect from whichever provider creds are set. |
| _provider creds_ | e.g. `STRIPE_SECRET_KEY`, `DODO_PAYMENTS_API_KEY`, `POLAR_ACCESS_TOKEN`, `CREEM_API_KEY`, … (see each adapter's `FromEnv`). |
| `SMOLBILL_CLOUD_MODE` | Upgrade funnel: `waitlist` (default, pre-launch) or `live` (buy button). |
| `SMOLBILL_CLOUD_URL` | Where the `/checkout` button points — your waitlist signup, or the Dodo checkout link when `live`. |
| `SMOLBILL_COMMERCIAL_URL` | Commercial-license CTA destination. |

## The upgrade funnel, by launch phase

The `/pricing` page and the `/checkout` button flip from a **waitlist** to a live
**buy** button with one env var — no redeploy of new code, just a secret change:

- **Pre-launch (build the audience):** `SMOLBILL_CLOUD_MODE=waitlist`,
  `SMOLBILL_CLOUD_URL=<your waitlist signup>`. `/checkout` sends people to the list.
- **Blow-up (start selling):** create a Dodo checkout link, then
  `SMOLBILL_CLOUD_MODE=live`, `SMOLBILL_CLOUD_URL=<dodo checkout link>`. `/checkout`
  now sends people to pay.

## MCP transports

- Local editors (Claude Code, Cursor, Cline, Windsurf, Zed): `smolbill mcp` (stdio).
- Remote clients (ChatGPT, claude.ai connectors): `smolbill mcp --http` serves the
  Streamable HTTP transport on `ADDR` at `/mcp`.

## Next: auto-recording payments (not yet wired)

Today `/checkout` hands off to the processor's hosted checkout; the processor's
dashboard is your record of who paid (fine for concierge onboarding). The next
step is a general **processor payment webhook** (`processor confirms payment →
mark the smolbill invoice paid`, idempotent, signature-verified) so payment status
updates automatically. It needs each processor's live webhook format — built once
we're transacting against a real key.
