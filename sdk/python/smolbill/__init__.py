"""smolbill — Python SDK for the open-source, provably-correct usage billing engine.

A thin, dependency-free wrapper over the /v1 HTTP API (stdlib urllib + hmac only).
Method keyword arguments are snake_case and map 1:1 to the wire — there is no
field-mapping layer, so the SDK cannot silently disagree with the API.

    from smolbill import Smolbill
    sb = Smolbill("http://localhost:8080")
    cus = sb.customers.create(name="Acme AI")
    sb.events.ingest(idempotency_key="e1", customer_id=cus["id"],
                     meter_code="tokens", properties={"n": 1000})
"""

from __future__ import annotations

import hashlib
import hmac
import json
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

__all__ = ["Smolbill", "SmolbillError", "sign", "verify_webhook"]
__version__ = "0.1.0"


class SmolbillError(Exception):
    """An API or signature error. `status` is the HTTP status (0 for local errors)."""

    def __init__(self, message: str, status: int, body: Any = None) -> None:
        super().__init__(message)
        self.status = status
        self.body = body


# ---------- webhook signature verification ----------

def sign(secret: str, body: bytes | str) -> str:
    """Reproduce the X-Smolbill-Signature value: hex HMAC-SHA256 of the body."""
    if isinstance(body, str):
        body = body.encode("utf-8")
    return hmac.new(secret.encode("utf-8"), body, hashlib.sha256).hexdigest()


def verify_webhook(*, secret: str, body: bytes | str, signature: str) -> dict[str, Any]:
    """Verify a delivery's signature (constant-time) and return the parsed event.

    Raises SmolbillError if the signature does not match, so a handler can trust
    event["type"] and event["data"].
    """
    expected = sign(secret, body)
    if not hmac.compare_digest(expected, signature or ""):
        raise SmolbillError("invalid webhook signature", 0)
    if isinstance(body, bytes):
        body = body.decode("utf-8")
    return json.loads(body)


# ---------- client ----------

class Smolbill:
    def __init__(self, base_url: str, *, api_key: str | None = None, timeout: float = 30.0) -> None:
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._timeout = timeout
        self.customers = _Customers(self)
        self.meters = _Meters(self)
        self.plans = _Plans(self)
        self.subscriptions = _Subscriptions(self)
        self.events = _Events(self)
        self.usage = _Usage(self)
        self.invoices = _Invoices(self)
        self.entitlements = _Entitlements(self)
        self.alerts = _Alerts(self)
        self.webhooks = _Webhooks(self)
        self.wallet = _Wallet(self)
        self.dunning = _Dunning(self)

    def _headers(self) -> dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self._api_key:
            h["Authorization"] = f"Bearer {self._api_key}"
        return h

    def _request(self, method: str, path: str, body: Any = None) -> Any:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(self._base_url + path, data=data, method=method, headers=self._headers())
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                text = resp.read().decode("utf-8")
                return json.loads(text) if text else None
        except urllib.error.HTTPError as e:
            text = e.read().decode("utf-8")
            parsed = json.loads(text) if text else {}
            msg = parsed.get("error") if isinstance(parsed, dict) else None
            raise SmolbillError(msg or f"HTTP {e.code}", e.code, parsed) from None

    def _verdict(self, path: str) -> dict[str, Any]:
        """GET a reconcile/verify endpoint where 409 is a drift verdict, not an error."""
        req = urllib.request.Request(self._base_url + path, method="GET", headers=self._headers())
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                parsed = json.loads(resp.read().decode("utf-8") or "{}")
                parsed["consistent"] = True
                return parsed
        except urllib.error.HTTPError as e:
            text = e.read().decode("utf-8")
            parsed = json.loads(text) if text else {}
            if e.code != 409:
                msg = parsed.get("error") if isinstance(parsed, dict) else None
                raise SmolbillError(msg or f"HTTP {e.code}", e.code, parsed) from None
            parsed["consistent"] = False
            return parsed

    def health(self) -> Any:
        return self._request("GET", "/healthz")


def _clean(d: dict[str, Any]) -> dict[str, Any]:
    """Drop None values so optional fields are omitted from the request body."""
    return {k: v for k, v in d.items() if v is not None}


class _Customers:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, name: str, external_id: str | None = None) -> Any:
        return self._c._request("POST", "/v1/customers", _clean({"name": name, "external_id": external_id}))


class _Meters:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, code: str, name: str, aggregation: str, property_key: str | None = None) -> Any:
        return self._c._request("POST", "/v1/meters",
                                _clean({"code": code, "name": name, "aggregation": aggregation, "property_key": property_key}))


class _Plans:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, name: str, prices: list[dict[str, Any]], version: int | None = None) -> Any:
        return self._c._request("POST", "/v1/plans", _clean({"name": name, "prices": prices, "version": version}))


class _Subscriptions:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, customer_id: str, plan_id: str, period_start: str, period_end: str,
               started_at: str | None = None) -> Any:
        return self._c._request("POST", "/v1/subscriptions", _clean({
            "customer_id": customer_id, "plan_id": plan_id,
            "period_start": period_start, "period_end": period_end, "started_at": started_at}))


class _Events:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def ingest(self, *, idempotency_key: str, customer_id: str, meter_code: str,
               properties: dict[str, Any], event_time: str | None = None) -> Any:
        return self._c._request("POST", "/v1/events", _clean({
            "idempotency_key": idempotency_key, "customer_id": customer_id, "meter_code": meter_code,
            "properties": properties, "event_time": event_time}))


class _Usage:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def get(self, customer_id: str, *, as_of: str | None = None) -> Any:
        path = f"/v1/usage/{urllib.parse.quote(customer_id)}"
        if as_of:
            path += f"?as_of={urllib.parse.quote(as_of)}"
        return self._c._request("GET", path)


class _Invoices:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def preview(self, *, subscription_id: str) -> Any:
        return self._c._request("POST", "/v1/invoices/preview", {"subscription_id": subscription_id})

    def simulate(self, *, customer_id: str, plan: dict[str, Any]) -> Any:
        return self._c._request("POST", "/v1/invoices/simulate", {"customer_id": customer_id, "plan": plan})

    def finalize(self, *, subscription_id: str) -> Any:
        return self._c._request("POST", "/v1/invoices/finalize", {"subscription_id": subscription_id})

    def reconcile(self, invoice_id: str) -> dict[str, Any]:
        return self._c._verdict(f"/v1/reconcile/{urllib.parse.quote(invoice_id)}")

    def verify(self, invoice_id: str) -> dict[str, Any]:
        return self._c._verdict(f"/v1/invoices/{urllib.parse.quote(invoice_id)}/verify")

    def collect(self, invoice_id: str) -> Any:
        """Attempt collection of an unpaid invoice now (manual dunning trigger)."""
        return self._c._request("POST", f"/v1/invoices/{urllib.parse.quote(invoice_id)}/collect")

    def collection(self, invoice_id: str) -> Any:
        """Inspect the dunning/recovery state of an invoice."""
        return self._c._request("GET", f"/v1/invoices/{urllib.parse.quote(invoice_id)}/collection")


class _Entitlements:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, customer_id: str, feature: str, kind: str, meter_code: str | None = None,
               limit_value: str | None = None, period_start: str | None = None, period_end: str | None = None) -> Any:
        return self._c._request("POST", "/v1/entitlements", _clean({
            "customer_id": customer_id, "feature": feature, "kind": kind, "meter_code": meter_code,
            "limit_value": limit_value, "period_start": period_start, "period_end": period_end}))

    def get(self, customer_id: str) -> Any:
        return self._c._request("GET", f"/v1/entitlements/{urllib.parse.quote(customer_id)}")


class _Alerts:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, customer_id: str, budget: str, currency: str, webhook_url: str,
               thresholds: list[int] | None = None) -> Any:
        return self._c._request("POST", "/v1/alerts", _clean({
            "customer_id": customer_id, "budget": budget, "currency": currency,
            "webhook_url": webhook_url, "thresholds": thresholds}))


class _Webhooks:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def create(self, *, url: str, events: list[str] | None = None) -> Any:
        return self._c._request("POST", "/v1/webhooks", _clean({"url": url, "events": events}))

    def list(self) -> Any:
        return self._c._request("GET", "/v1/webhooks")


class _Wallet:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def topup(self, customer_id: str, *, amount: str, currency: str, reason: str | None = None,
              idempotency_key: str | None = None) -> Any:
        return self._c._request("POST", f"/v1/wallet/{urllib.parse.quote(customer_id)}/topup", _clean({
            "amount": amount, "currency": currency, "reason": reason, "idempotency_key": idempotency_key}))

    def get(self, customer_id: str) -> Any:
        return self._c._request("GET", f"/v1/wallet/{urllib.parse.quote(customer_id)}")


class _Dunning:
    def __init__(self, c: Smolbill) -> None:
        self._c = c

    def run(self) -> Any:
        """Process every due collection — the endpoint a cron hits on a cadence."""
        return self._c._request("POST", "/v1/dunning/run")
