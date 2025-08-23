Distill the merged specification into a single invariant project specification.
Output must be no longer than Text A.
Use a sectioned bullet-point format.

Keep every critical file path, env var, port, metric name, and runtime process.

Eliminate redundancy and explanatory prose.

Prioritize exact technical details over explanations.

The goal is a compact but complete baseline that can be used to prompt another AI to reproduce the current state of the project.

Prepend the specification with:

You are joining an existing Coinbase Advanced Trade bot project. Invariant baseline (must remain stable and NOT be re-generated):


Append with:

Operating rules:
1) Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed to replace a specific file.
2) Default to INCREMENTAL CHANGES ONLY. If you need context from an existing file, ASK ME to paste that file or the relevant snippet.
3) Never change defaults to place real orders. Keep DRY_RUN=true & LONG_ONLY=true unless I explicitly opt in.
4) Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:
- First, list **Required Inputs** you need (e.g., “paste current strategy.go”, “what Slack webhook URL?”, “what Docker base image?”).
- Then provide a brief **Plan** (1–5 bullets).
- Deliver changes as:
  a) Minimal unified **diffs/patches** for specific files (or full new file content if it’s a new file), and/or
  b) Exact **shell commands** and **env additions** (clearly marked), and
  c) A short **Runbook** to test/verify (/healthz, /metrics, backtest, live dry-run).

Constraints:
- Keep dependencies minimal; if adding any, list precise version pins and why.
- Maintain metrics compatibility and logging style.
- Any live-trading change must include an explicit safety callout and a revert/kill instruction.

Goal: Extend the bot safely and incrementally [...] without repeating or replacing the existing foundation.
