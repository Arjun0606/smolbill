# Security policy

smolbill handles money math, invoicing, and (optionally) credentials for payment
processors, so security reports are taken seriously.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.** Instead, report it
privately:

- Open a private advisory: **GitHub → Security → Report a vulnerability** on this
  repo, or
- Email **karjunvarma2001@gmail.com** with details and, if possible, a proof of
  concept.

You'll get an acknowledgement within a few days. Once a fix ships, you're welcome
to be credited (or stay anonymous, your call).

## What's in scope

The things most worth looking at:

- **Money correctness** — any input that makes the engine over-bill, double-count,
  drop an event, or produce an invoice that doesn't reconcile against its events.
- **Auth** — bypassing the API-key check on `/v1` or `/mcp`, or the MCP armed-money
  guardrail (a path where an agent moves real money without the engine being armed).
- **Determinism** — anything that breaks "same events in → same invoice → same hash".
- **Standard web issues** — injection, SSRF via webhook URLs, signature-verification
  flaws on inbound/outbound webhooks, resource exhaustion.

## What's not

- Reports that require a malicious operator (someone who already has your API keys
  or runs the binary) — the operator is trusted by design.
- Missing hardening on a deployment you left intentionally open (e.g. running with
  `SMOLBILL_API_KEYS` unset, which the binary warns about on startup).

## Supported versions

This is pre-1.0 software. Security fixes land on `main`; pin a commit if you need
stability and watch releases for advisories.
