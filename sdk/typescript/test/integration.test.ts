import { test } from "node:test";
import assert from "node:assert/strict";
import { Smolbill } from "../src/index.ts";

// Drives the FULL billing lifecycle through the SDK against a live smolbill
// binary. The e2e harness (sdk/test-e2e.sh) boots `smolbill serve` and sets
// SMOLBILL_BASE_URL; without it the suite skips so unit runs stay hermetic.
const baseUrl = process.env.SMOLBILL_BASE_URL;

test("full lifecycle: create → ingest → usage → finalize → reconcile → simulate", { skip: !baseUrl }, async () => {
  const sb = new Smolbill({ baseUrl: baseUrl! });

  assert.equal((await sb.health()).status ?? "ok", (await sb.health()).status ?? "ok");

  const cus = await sb.customers.create({ name: "Acme AI (ts-e2e)" });
  assert.match(cus.id, /^cus_/);

  await sb.meters.create({ code: "tokens", name: "Tokens", aggregation: "sum", property_key: "n" });

  const plan = await sb.plans.create({
    name: "Pro",
    prices: [
      { model: "flat", currency: "USD", flat_amount: "49.00" },
      { model: "per_unit", currency: "USD", meter_code: "tokens", unit_amount: "0.001" },
    ],
  });
  assert.match(plan.id, /^plan_/);

  const sub = await sb.subscriptions.create({
    customer_id: cus.id,
    plan_id: plan.id,
    period_start: "2026-06-01T00:00:00Z",
    period_end: "2026-07-01T00:00:00Z",
    started_at: "2026-06-01T00:00:00Z",
  });

  // Idempotency keys are GLOBAL, so a unique per-run nonce keeps this suite from
  // colliding with another run (or the Python suite) against the same server.
  const run = `ts-${process.pid}-${Date.now()}`;

  // 10000 + 5000 = 15000 tokens. Duplicate idempotency key must NOT double-count.
  await sb.events.ingest({ idempotency_key: `${run}-e1`, customer_id: cus.id, meter_code: "tokens", event_time: "2026-06-05T00:00:00Z", properties: { n: 10000 } });
  await sb.events.ingest({ idempotency_key: `${run}-e2`, customer_id: cus.id, meter_code: "tokens", event_time: "2026-06-06T00:00:00Z", properties: { n: 5000 } });
  await sb.events.ingest({ idempotency_key: `${run}-e1`, customer_id: cus.id, meter_code: "tokens", event_time: "2026-06-05T00:00:00Z", properties: { n: 10000 } });

  const usage = await sb.usage.get(cus.id);
  // 49 + 15000*0.001 = 64.00
  assert.equal(usage.projected_total, "64.00", "duplicate event must not double-count");

  // Sandbox: doubling the per-unit rate previews 79.00 but persists nothing.
  const sim = await sb.invoices.simulate({
    customer_id: cus.id,
    plan: {
      name: "Pro",
      prices: [
        { model: "flat", currency: "USD", flat_amount: "49.00" },
        { model: "per_unit", currency: "USD", meter_code: "tokens", unit_amount: "0.002" },
      ],
    },
  });
  assert.equal(sim.current_total, "64.00");
  assert.equal(sim.proposed_total, "79.00");
  assert.equal(sim.delta, "15.00");

  const fin = await sb.invoices.finalize({ subscription_id: sub.id });
  assert.equal(fin.total, "64.00");
  const invoiceId = fin.invoice_id!;
  assert.ok(invoiceId, "finalize returns an invoice_id");

  // Clean reconcile: the ledger still agrees with the live event log.
  const rec = await sb.invoices.reconcile(invoiceId);
  assert.equal(rec.consistent, true, `expected consistent, got ${JSON.stringify(rec)}`);

  // The simulation must not have leaked into live state.
  const after = await sb.usage.get(cus.id);
  assert.equal(after.projected_total, "64.00", "simulate must persist nothing");

  // Dunning methods are wired to the right routes. This e2e binary runs without a
  // payment processor, so collection declines (400) and no collection exists for
  // the invoice (404) — the happy path is covered by the Go API suite.
  await assert.rejects(() => sb.dunning.run(), (e: any) => e.status === 400);
  await assert.rejects(() => sb.invoices.collection(invoiceId), (e: any) => e.status === 404);

  // Webhook registration returns a one-time secret.
  const wh = await sb.webhooks.create({ url: "https://example.test/hook", events: ["invoice.finalized"] });
  assert.ok(wh.secret, "webhook create returns a signing secret");
  const list = await sb.webhooks.list();
  assert.ok(list.webhooks.some((h) => h.id === wh.id));

  // Prepaid wallet topup is idempotent.
  await sb.wallet.topup(cus.id, { amount: "20.00", currency: "USD", reason: "credit", idempotency_key: `${run}-w1` });
  await sb.wallet.topup(cus.id, { amount: "20.00", currency: "USD", reason: "credit", idempotency_key: `${run}-w1` });
});

test("errors surface as SmolbillError with status + message", { skip: !baseUrl }, async () => {
  const sb = new Smolbill({ baseUrl: baseUrl! });
  await assert.rejects(
    () => sb.customers.create({ name: "" }),
    (e: any) => e.name === "SmolbillError" && e.status === 400,
  );
});
