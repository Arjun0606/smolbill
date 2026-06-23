# Commercial license

smolbill's engine is licensed under the **GNU AGPLv3** (see [LICENSE](LICENSE)).
The AGPL is real, OSI-approved open source — self-host it, read it, fork it, ship
it. For most people that's all they'll ever need, free forever.

The AGPL has one obligation: if you **modify** smolbill and let other people use it
over a network, or you **distribute** it, you must release your corresponding
source under the AGPL too. A **commercial license removes that obligation.**

## You need a commercial license if you…

- **embed smolbill in a closed-source product** you ship or host, and you don't want
  to release that product's source under the AGPL;
- **offer smolbill (modified) to third parties as a service** without open-sourcing
  your changes;
- have a **company policy that forbids AGPL/GPL** code (most enterprises do) and need
  a permissive grant to use smolbill at all;
- want a **written warranty / indemnity** and a support SLA.

You do **not** need one to: self-host smolbill unmodified for your own internal use,
evaluate it, contribute to it, or build on the **Apache-2.0 client SDKs** under
`sdk/` (those carry no AGPL obligation — see `sdk/<lang>/LICENSE`).

## What the commercial license grants

- A non-AGPL, royalty-free right to use, modify, and embed the smolbill engine in
  your own products and services, with no copyleft obligation on your code.
- Access to the **closed-source Pro features** (revenue analytics, SSO/RBAC,
  cross-merchant ML retry timing, card-account-updater / network tokens, the hosted
  MCP HTTP endpoint, audit-log retention) — these are not part of the AGPL repo.
- Support and a written license agreement.

## How to buy

Self-serve, no sales call. The Cloud and commercial plans are purchased through a
Dodo checkout (smolbill dogfoods smolbill to bill them):

- **Cloud (managed)** — flat fee + usage, we run it for you. → smolbill.com/pricing
- **Commercial license (self-host, no AGPL)** — per-company annual. → smolbill.com/pricing

Questions about scope? Open a GitHub Discussion or email the address in
`smolbill.com/pricing`. (We don't gate evaluation behind a call.)

> Why dual-license? It's the only model that keeps the engine genuinely open while
> staying alive: hobbyists and OSS projects use the AGPL for free, and companies that
> can't take AGPL fund the work. The license is the business model — not a crippled
> "community edition."
