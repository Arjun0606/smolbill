# smolbill-mcp

Connect your AI agent to [smolbill](../../README.md) in one command and set up your
whole billing **by talking** — no JSON config with an absolute binary path, no docs.

```jsonc
// claude_desktop_config.json (or your Cursor MCP config)
{
  "mcpServers": {
    "smolbill": {
      "command": "npx",
      "args": ["-y", "smolbill-mcp"],
      "env": { "DATABASE_URL": "postgres://…" }   // omit for in-memory
    }
  }
}
```

That's it. The agent now has the full smolbill toolset. Then just say:

> set up usage billing for my AI app — $0.001 per token with a $20 monthly base

and it runs `quickstart_billing` and you have a working plan and a previewable
invoice in one step. Or talk it through piece by piece — create customers, meters,
plans, record usage, finalize, reconcile, run dunning, read analytics. Every feature
is a tool.

## Requirements

The `smolbill` binary must be installed (see the main repo) or reachable via the
`SMOLBILL_BIN` environment variable. `DATABASE_URL` and `STRIPE_SECRET_KEY` pass
straight through. With `STRIPE_SECRET_KEY` set, the agent can also verify invoices
against the processor and run dunning.

## Why this is different

smolbill's MCP isn't bolted on — the agent and the REST API compute through the
**same engine**, so they can never disagree, and the tools are **intent-only** (there
is no `charge()` tool; a hallucinated decimal can never touch money). You get the
correctness of a deterministic billing engine with the ease of just describing what
you want.

Apache-2.0.
