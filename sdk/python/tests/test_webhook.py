"""Cross-language webhook signing contract.

The golden digests asserted here are byte-identical to the Go suite
(internal/webhook TestSignGoldenValues) and the TypeScript suite — proof that all
three languages sign a webhook the exact same way.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from smolbill import SmolbillError, sign, verify_webhook  # noqa: E402

EVENT_BODY = '{"id":"evt_1","type":"invoice.finalized","occurred_at":"1970-01-01T00:00:00Z","data":{"invoice_id":"inv_1"}}'
EVENT_SIG = "5863bd53484af21871b474ad7bc43be865f97d840768001f6ceee4050cd9cce2"


class TestWebhookSigning(unittest.TestCase):
    def test_sign_matches_golden(self):
        self.assertEqual(sign("whsec_test", EVENT_BODY), EVENT_SIG)
        self.assertEqual(sign("s", "hello"), "8a06a64224d83d1bf0a2140fab7d5b462cf3f450592c7963910ed331e9031056")

    def test_verify_returns_event(self):
        evt = verify_webhook(secret="whsec_test", body=EVENT_BODY, signature=EVENT_SIG)
        self.assertEqual(evt["type"], "invoice.finalized")
        self.assertEqual(evt["data"]["invoice_id"], "inv_1")

    def test_verify_rejects_tampered_body(self):
        with self.assertRaises(SmolbillError):
            verify_webhook(secret="whsec_test", body=EVENT_BODY + " ", signature=EVENT_SIG)

    def test_verify_rejects_wrong_secret(self):
        with self.assertRaises(SmolbillError):
            verify_webhook(secret="whsec_wrong", body=EVENT_BODY, signature=EVENT_SIG)

    def test_verify_rejects_empty_signature(self):
        with self.assertRaises(SmolbillError):
            verify_webhook(secret="whsec_test", body=EVENT_BODY, signature="")

    def test_sign_accepts_bytes(self):
        self.assertEqual(sign("whsec_test", EVENT_BODY.encode()), EVENT_SIG)


if __name__ == "__main__":
    unittest.main()
