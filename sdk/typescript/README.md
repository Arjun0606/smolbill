# smolbill — TypeScript SDK

A thin, fully-typed, **zero-dependency** client for [smolbill](../../README.md), the open-source provably-correct usage billing engine. Built on Node's global `fetch` and built-in `crypto`.

```sh
npm install smolbill
```

## Five lines to a metered bill

```ts
import { Smolbill } from "smolbill";

const sb = new Smolbill({ baseUrl: "http://localhost:8080" });

const cus = await sb.customers.create({ name: "Acme AI" });
await sb.meters.create({ code: "tokens", name: "Tokens", aggregation: "sum", property_key: "n" });
await sb.events.ingest({ idempotency_key: "req-1", customer_id: cus.id, meter_code: "tokens", properties: { n: 1000 } });
const usage = await sb.usage.get(cus.id); // { projected_total: "1.00", ... }
```

Parameters and responses are **snake_case, mirroring the wire 1:1** (the same choice Stripe's SDK makes) — there's no field-mapping layer, so the client can't silently disagree with the API. Money is always a currency-correct string (`"64.00"` for USD, `"64"` for JPY).

## Verifying webhooks

`invoice.finalized` and `drift.detected` are delivered signed (`X-Smolbill-Signature`, HMAC-SHA256). `verifyWebhook` checks the signature in constant time and hands you the typed event:

```ts
import { verifyWebhook } from "smolbill";

app.post("/webhooks", express.raw({ type: "application/json" }), (req, res) => {
  const evt = verifyWebhook({
    secret: process.env.SMOLBILL_WHSEC!,           // returned once when you created the webhook
    body: req.body.toString("utf8"),               // the RAW body, not a re-stringified object
    signature: req.header("X-Smolbill-Signature")!,
  });
  if (evt.type === "drift.detected") pageOnCall(evt.data.invoice_id);
  res.sendStatus(200);
});
```

A bad signature throws `SmolbillError` — so inside the handler you can trust `evt.type` and `evt.data`.

## Notes

- **`reconcile` / `verify` return a verdict, not an error.** A drift (HTTP 409) is expected output; the result has `consistent: false` plus the line-level diff. Every other call throws `SmolbillError` (with `.status` and `.body`) on a non-2xx.
- **Idempotency keys are global.** Reuse the same `idempotency_key` to make an ingest safely retryable; use a fresh one per distinct event.

Run `node --test --experimental-strip-types test/*.test.ts` for the unit suite. The full lifecycle is exercised against a live binary by [`sdk/test-e2e.sh`](../test-e2e.sh).

Apache-2.0.
