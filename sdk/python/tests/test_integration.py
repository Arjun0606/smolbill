"""Full billing lifecycle through the Python SDK against a live smolbill binary.

The e2e harness (sdk/test-e2e.sh) boots `smolbill serve` and sets
SMOLBILL_BASE_URL; without it the suite skips so unit runs stay hermetic.
"""

import os
import sys
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from smolbill import Smolbill, SmolbillError  # noqa: E402

BASE_URL = os.environ.get("SMOLBILL_BASE_URL")


@unittest.skipUnless(BASE_URL, "set SMOLBILL_BASE_URL (or run sdk/test-e2e.sh)")
class TestLifecycle(unittest.TestCase):
    def test_full_lifecycle(self):
        sb = Smolbill(BASE_URL)

        cus = sb.customers.create(name="Acme AI (py-e2e)")
        self.assertTrue(cus["id"].startswith("cus_"))

        sb.meters.create(code="tokens", name="Tokens", aggregation="sum", property_key="n")

        plan = sb.plans.create(name="Pro", prices=[
            {"model": "flat", "currency": "USD", "flat_amount": "49.00"},
            {"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.001"},
        ])
        self.assertTrue(plan["id"].startswith("plan_"))

        sub = sb.subscriptions.create(
            customer_id=cus["id"], plan_id=plan["id"],
            period_start="2026-06-01T00:00:00Z", period_end="2026-07-01T00:00:00Z",
            started_at="2026-06-01T00:00:00Z",
        )

        # Idempotency keys are GLOBAL; a unique per-run nonce keeps this suite from
        # colliding with another run (or the TS suite) against the same server.
        run = f"py-{os.getpid()}-{int(time.time() * 1000)}"

        # 10000 + 5000 = 15000 tokens; the duplicate key must NOT double-count.
        sb.events.ingest(idempotency_key=f"{run}-e1", customer_id=cus["id"], meter_code="tokens",
                         event_time="2026-06-05T00:00:00Z", properties={"n": 10000})
        sb.events.ingest(idempotency_key=f"{run}-e2", customer_id=cus["id"], meter_code="tokens",
                         event_time="2026-06-06T00:00:00Z", properties={"n": 5000})
        sb.events.ingest(idempotency_key=f"{run}-e1", customer_id=cus["id"], meter_code="tokens",
                         event_time="2026-06-05T00:00:00Z", properties={"n": 10000})

        usage = sb.usage.get(cus["id"])
        self.assertEqual(usage["projected_total"], "64.00")  # 49 + 15000*0.001

        sim = sb.invoices.simulate(customer_id=cus["id"], plan={
            "name": "Pro", "prices": [
                {"model": "flat", "currency": "USD", "flat_amount": "49.00"},
                {"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.002"},
            ]})
        self.assertEqual(sim["current_total"], "64.00")
        self.assertEqual(sim["proposed_total"], "79.00")
        self.assertEqual(sim["delta"], "15.00")

        fin = sb.invoices.finalize(subscription_id=sub["id"])
        self.assertEqual(fin["total"], "64.00")
        invoice_id = fin["invoice_id"]

        rec = sb.invoices.reconcile(invoice_id)
        self.assertTrue(rec["consistent"])

        after = sb.usage.get(cus["id"])
        self.assertEqual(after["projected_total"], "64.00")  # simulate persisted nothing

        # Analytics reflects the finalized invoice we just created. (Totals
        # accumulate across the shared e2e server, so assert presence + a floor.)
        an = sb.analytics.get()
        self.assertGreaterEqual(an["customers"], 1)
        self.assertGreaterEqual(an["finalized_invoices"], 1)
        self.assertGreaterEqual(float(an["finalized_revenue"]["USD"]), 64.0)

        # Dunning message templates: 4 defaults, and preview renders sample data.
        tmpls = sb.dunning.templates()
        self.assertEqual(len(tmpls["templates"]), 4)
        msg = sb.dunning.preview("recovered")
        self.assertTrue(msg["subject"])
        self.assertIn("64.00", msg["body"])

        # Dunning methods are wired to the right routes. This e2e binary runs
        # without a processor, so collection declines (400) and no collection
        # exists for the invoice (404) — happy path is covered by the Go suite.
        with self.assertRaises(SmolbillError) as run_err:
            sb.dunning.run()
        self.assertEqual(run_err.exception.status, 400)
        with self.assertRaises(SmolbillError) as col_err:
            sb.invoices.collection(invoice_id)
        self.assertEqual(col_err.exception.status, 404)

        wh = sb.webhooks.create(url="https://example.test/hook", events=["invoice.finalized"])
        self.assertTrue(wh["secret"])
        self.assertTrue(any(h["id"] == wh["id"] for h in sb.webhooks.list()["webhooks"]))

        sb.wallet.topup(cus["id"], amount="20.00", currency="USD", reason="credit", idempotency_key=f"{run}-w1")
        sb.wallet.topup(cus["id"], amount="20.00", currency="USD", reason="credit", idempotency_key=f"{run}-w1")

    def test_error_surfaces(self):
        sb = Smolbill(BASE_URL)
        with self.assertRaises(SmolbillError) as ctx:
            sb.customers.create(name="")
        self.assertEqual(ctx.exception.status, 400)


if __name__ == "__main__":
    unittest.main()
