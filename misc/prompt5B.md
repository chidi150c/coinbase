Prepend the specification with:

You are joining an existing Coinbase Advanced Trade bot project. Invariant baseline (must remain stable and NOT be re-generated):


Append with:

Operating rules:

Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed.

Default to INCREMENTAL CHANGES ONLY; ask for file context if needed.

Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:

Provide a brief Plan.

1) For each item in the plan, first output a single-sentence CHANGE_DESCRIPTION:
   - Format: verb + what is being changed + how/with what.
   - Imperative style (e.g., “add…”, “update…”, “remove…”).
   - Describe exactly the code change required; avoid general context.
   - Avoid vague terms like “improve/enhance/fix” unless paired with the specific element being changed.
   - One sentence per item only.

2) Pause and verify required inputs. If ANY input is missing, ask me to provide it before writing code.
   - Examples of required inputs:
     - Source files: e.g., “paste current live.go / trader.go / strategy.go / model.go / metrics.go / config.go / env.go / backtest.go / broker*.go / bridge/app.py / monitoring/docker-compose.yml”.
     - Env/config values: e.g., “what Slack webhook URL?”, “what Docker base image?”, “list current /opt/coinbase/env/bot.env and bridge.env”.
     - External URLs/IDs (API keys should never be pasted in clear; ask me to confirm they are already configured).
   - Never guess; explicitly request missing files or settings.

3) After all inputs are provided, generate the code:
   - Output the complete updated file(s), copy-paste ready.
   - Apply only minimal edits needed to satisfy the CHANGE_DESCRIPTION(s).
   - Do not rename or remove existing functions, structs, metrics, environment keys, log strings, CLI flags, routes, or file paths unless the plan item explicitly requires it.
   - Maintain metrics compatibility and logging style.
   - Keep dependencies minimal; if adding any, list the precise versions and justify them in one line.

4) Safety & operations rules:
   - If a change affects live trading behavior, include an explicit SAFETY CALLOUT and REVERT INSTRUCTIONS (exact env changes or commands to roll back).
   - Provide a short runbook: required env edits, shell commands, restart instructions, and verification steps (health checks/metrics queries).
   - All changes must extend the bot safely and incrementally, without rewriting or replacing the existing foundation.

Example workflow:
Plan item: “fix startup equity spike in live.go.”
1. CHANGE_DESCRIPTION: `update initLiveEquity and per-tick equity refresh to skip setting trader equity until bridge accounts return a valid value, logging a waiting message instead`
2. Ask: “Please paste your current live.go so I can apply the change.”
3. After file is provided, output the complete updated live.go with only the minimal edits to implement the description, plus:
   - Safety callout (no risk to live trading; behavior is deferred until balances are available).
   - Revert instructions (restore previous lines X–Y to set equity immediately).
   - Runbook (commands to rebuild/restart and how to verify via /metrics and logs).