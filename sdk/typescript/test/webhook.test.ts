import { test } from "node:test";
import assert from "node:assert/strict";
import { sign, verifyWebhook, SmolbillError } from "../src/index.ts";

// These golden digests are identical to the ones asserted by the Go suite
// (internal/webhook TestSignGoldenValues) and the Python suite — proof that all
// three languages sign a webhook the exact same way.
const EVENT_BODY = `{"id":"evt_1","type":"invoice.finalized","occurred_at":"1970-01-01T00:00:00Z","data":{"invoice_id":"inv_1"}}`;
const EVENT_SIG = "5863bd53484af21871b474ad7bc43be865f97d840768001f6ceee4050cd9cce2";

test("sign matches the Go/Python golden values", () => {
  assert.equal(sign("whsec_test", EVENT_BODY), EVENT_SIG);
  assert.equal(sign("s", "hello"), "8a06a64224d83d1bf0a2140fab7d5b462cf3f450592c7963910ed331e9031056");
});

test("verifyWebhook returns the typed event on a valid signature", () => {
  const evt = verifyWebhook({ secret: "whsec_test", body: EVENT_BODY, signature: EVENT_SIG });
  assert.equal(evt.type, "invoice.finalized");
  assert.equal(evt.data.invoice_id, "inv_1");
});

test("verifyWebhook throws on a tampered body", () => {
  assert.throws(
    () => verifyWebhook({ secret: "whsec_test", body: EVENT_BODY + " ", signature: EVENT_SIG }),
    (e: unknown) => e instanceof SmolbillError,
  );
});

test("verifyWebhook throws on a wrong secret", () => {
  assert.throws(
    () => verifyWebhook({ secret: "whsec_wrong", body: EVENT_BODY, signature: EVENT_SIG }),
    (e: unknown) => e instanceof SmolbillError,
  );
});

test("verifyWebhook throws on an empty/short signature without crashing", () => {
  assert.throws(() => verifyWebhook({ secret: "whsec_test", body: EVENT_BODY, signature: "" }));
});
