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

Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed.

Default to INCREMENTAL CHANGES ONLY; ask for file context if needed.

Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:

List Required Inputs you need.

Provide a brief Plan.

Deliver changes as diffs/patches, shell commands/env additions, and short runbooks.

Constraints:

Keep dependencies minimal; list precise versions if added.

Maintain metrics compatibility and logging style.

Any live-trading change must include explicit safety callout and revert instructions.

Goal:
Extend the bot safely and incrementally while implementing this Phase, without repeating or replacing the existing foundation.
