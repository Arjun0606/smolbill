# Contributing to smolbill

Thanks for wanting to help. A few things keep this project healthy and keep it
*possible* to fund the work.

## Developer Certificate of Origin (DCO)

smolbill is **dual-licensed**: the engine is AGPLv3, and a commercial license is
offered to organizations that can't use the AGPL (see [COMMERCIAL.md](COMMERCIAL.md)).
For that to be legal, every contribution must be one the project can ship under
**both** licenses.

So we use the [Developer Certificate of Origin](https://developercertificate.org/):
by signing off on a commit, you certify you wrote the patch (or have the right to
submit it) and that you license your contribution under the project's terms,
including the right to offer it under the commercial license. Sign off with:

```
git commit -s -m "your message"
```

This adds a `Signed-off-by: Your Name <you@example.com>` line. PRs without sign-off
can't be merged. (This is the same lightweight model the Linux kernel and GitLab
use — no separate CLA paperwork.)

## Ground rules that protect correctness

smolbill bills real money, so a few invariants are non-negotiable — a PR that breaks
one will be sent back:

- **No floats in money math.** All amounts go through `internal/money` (exact
  decimals, integer minor units on the wire). A hallucinated decimal ends a business
  relationship.
- **The engine never imports a concrete payment processor.** New rails implement the
  `payments.Processor` interface behind `internal/payments/providers`; the engine
  depends only on the interface.
- **Determinism holds.** Same inputs → same invoice → same reconciliation hash. If
  you touch the engine, the determinism and reconciliation tests must still pass.
- **No money-math MCP tool.** The agent passes intent; the deterministic engine
  computes every cent. There is deliberately no `charge()` / `calculate_bill()` tool.

## Before you open a PR

```
gofmt -w . && go vet ./... && go test ./...
```

Keep changes focused, match the surrounding style, and add a test for any behavior
change. Thanks for building with us.
