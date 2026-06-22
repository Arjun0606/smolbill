# smolbill — Python SDK

A thin, **dependency-free** client for [smolbill](../../README.md), the open-source provably-correct usage billing engine. Standard library only (`urllib` + `hmac`).

```sh
pip install smolbill
```

## Five lines to a metered bill

```python
from smolbill import Smolbill

sb = Smolbill("http://localhost:8080")

cus = sb.customers.create(name="Acme AI")
sb.meters.create(code="tokens", name="Tokens", aggregation="sum", property_key="n")
sb.events.ingest(idempotency_key="req-1", customer_id=cus["id"], meter_code="tokens", properties={"n": 1000})
usage = sb.usage.get(cus["id"])  # {"projected_total": "1.00", ...}
```

Keyword arguments are snake_case and map **1:1 to the wire** — no field-mapping layer, so the client can't silently disagree with the API. Money is always a currency-correct string (`"64.00"` for USD, `"64"` for JPY).

## Verifying webhooks

`invoice.finalized` and `drift.detected` are delivered signed (`X-Smolbill-Signature`, HMAC-SHA256). `verify_webhook` checks the signature in constant time and returns the parsed event:

```python
from smolbill import verify_webhook, SmolbillError

@app.post("/webhooks")
def hook(request):
    try:
        evt = verify_webhook(
            secret=os.environ["SMOLBILL_WHSEC"],          # returned once when you created the webhook
            body=request.get_data(),                      # the RAW body bytes, not a re-serialized dict
            signature=request.headers["X-Smolbill-Signature"],
        )
    except SmolbillError:
        return ("bad signature", 400)
    if evt["type"] == "drift.detected":
        page_on_call(evt["data"]["invoice_id"])
    return ("", 200)
```

## Notes

- **`reconcile` / `verify` return a verdict, not an error.** A drift (HTTP 409) is expected; the dict has `["consistent"] == False` plus the line-level diff. Every other call raises `SmolbillError` (with `.status` and `.body`) on a non-2xx.
- **Idempotency keys are global.** Reuse the same `idempotency_key` to make an ingest safely retryable; use a fresh one per distinct event.

Run `python3 -m unittest discover -s tests` for the unit suite. The full lifecycle is exercised against a live binary by [`sdk/test-e2e.sh`](../test-e2e.sh).

Apache-2.0.
