#!/usr/bin/env node
// One-command MCP launcher for smolbill. Runs the smolbill binary's `mcp`
// subcommand over stdio so an agent (Claude Desktop, Cursor) can drive your
// billing. Connecting is `npx smolbill-mcp` instead of hand-editing a config with
// an absolute binary path.
//
//   claude_desktop_config.json:
//   { "mcpServers": { "smolbill": { "command": "npx", "args": ["-y", "smolbill-mcp"],
//       "env": { "DATABASE_URL": "postgres://…" } } } }
//
// The smolbill binary must be installed (https://github.com/Arjun0606/smolbill) or
// reachable via SMOLBILL_BIN. DATABASE_URL / STRIPE_SECRET_KEY pass straight
// through the environment.
import { spawn } from "node:child_process";

const bin = process.env.SMOLBILL_BIN || "smolbill";
const child = spawn(bin, ["mcp"], { stdio: "inherit", env: process.env });

child.on("error", (err) => {
  if (err && err.code === "ENOENT") {
    process.stderr.write(
      `smolbill: binary "${bin}" not found.\n` +
        `Install it: https://github.com/Arjun0606/smolbill (or set SMOLBILL_BIN to its path),\n` +
        `then: npx smolbill-mcp\n`,
    );
    process.exit(127);
  }
  throw err;
});

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 0);
});
