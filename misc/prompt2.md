Here is baseline text A: [...]
Here is update text B: [...]

Task:
Please integrate B directly inside A where it naturally belongs (not as a separate list).

Keep all of A’s original lines intact unless they must be expanded to fit B.

Do not delete or paraphrase A — if you must replace a line, show the removed text separately at the end under a “Dropped / Replaced Lines” section.

The result must read as a single, seamless, stable specification.

Expected Outcome:

The merged spec (A + B, with inline integration and dropped/replaced lines noted) is the current truth of the project at the end of this Phase

Merged Spec = A + B woven together into one cohesive, stable specification.

Rules:

The merged document is now the authoritative single source:

It preserves every invariant line from A unless it was explicitly superseded.

It embeds all of B’s details (new files, env keys, ports, commands, metrics, runtime behaviors).

The “Dropped / Replaced Lines” section is only for traceability — so you know exactly what changed from the original baseline.