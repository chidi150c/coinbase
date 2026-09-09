You are modifying a Go-based Binance spot-trading bot and its BOT OPS monitoring console.

Your task is to create a complete, production-ready entry producer from a single supplied runtime log snapshot representing a favorable trade opportunity the bot should have recognized.

The user’s only required input is the log snapshot. Derive every other proposal from that evidence and the authoritative current codebase. Do not ask the user for information that can be established from the snapshot or code.

Do not implement immediately. First complete the producer design, identify every material decision, and obtain the user’s approval. After approval, request all current files required for an end-to-end implementation and mutate them together.

## Core rule

Producer creation is end-to-end. A producer is not complete merely because its signal evaluator compiles. It must participate correctly in evaluation, allocation, execution, persistence, economics, continuation, Refund service and BOT OPS observability.

## Phase 1: Analyze the supplied point

Extract and report:

- Exact UTC timestamp.
- Price.
- AI raw direction.
- Confidence.
- Market regime and multiplier.
- MACD line, previous line, histogram, derivatives, momentum and strong-state flags.
- EMA spread, EMA 20/50 relationship and pattern flags.
- Recent high and low.
- Distance from the relevant recent extreme.
- Pyramid spacing, adverse, gate, latch and effective gate values.
- Existing producer decisions.
- Producer candidate count.
- Allocation outcome.
- Available resource values.
- Pending and exit conditions.
- Refund or reconciliation activity.

Separate signal evidence from unrelated operational activity. Equity state, Refund state, existing exits and resource shortages must not become signal gates unless they directly define the new producer’s intended market thesis.

## Phase 2: Establish the market thesis

Determine whether the point represents a coherent BUY or SELL opportunity.

Describe the opportunity in plain language, such as:

- Bottom reversal.
- Peak reversal.
- Downtrend recovery.
- Uptrend continuation.
- Normal-regime rollover.
- Momentum exhaustion.
- Buffered-latch entry.

Do not mechanically turn every logged field into a condition. Select only conditions that describe a repeatable market setup.

If the snapshot is internally contradictory, identify the contradictions and explain which conditions should dominate. Do not conceal uncertainty.

## Phase 3: Check existing producers

Compare the snapshot with every existing producer.

For each relevant producer, state:

- Conditions that passed.
- Conditions that failed.
- Whether the failure was intentional.
- Whether adjusting that producer would preserve its original thesis.
- Whether a genuinely new producer is justified.

Do not silently weaken an existing producer whose purpose differs from the supplied opportunity.

## Phase 4: Propose the producer contract

Provide:

```text
Name:
Friendly label:
Side:
Market thesis:
Tier:
Priority:

```

Apply these defaults:

- Every new producer starts at `LOW` tier.
- Every new producer receives the next integer priority below the current lowest registered priority.
- Priorities descend one point at a time toward `1`.
- Priority `0` is reserved for unknown or unregistered producers.
- Tier or priority promotion requires performance evidence and explicit user approval.

Define the exact first-entry Boolean expression.

Every condition must:

- Be supported by the supplied snapshot.
- Represent the stated market thesis.
- Be reusable over an area of market state rather than overfit one exact timestamp or price.
- Use named constants for configurable thresholds.

The supplied snapshot must evaluate to `true` under the proposed expression.

## Phase 5: Define the price area

Never define the opportunity as one exact price point.

Create a bounded or one-sided price region appropriate to the direction.

For BUY:

- Lower prices are more favorable.
- Do not reject a price merely because it moved below a BUY latch or threshold unless a separately justified safety floor exists.
- A typical favorable-direction gate is:

```go
price <= upperBuyBoundary

```

For SELL:

- Higher prices are more favorable.
- Do not reject a price merely because it moved above a SELL latch or threshold unless a separately justified safety ceiling exists.
- A typical favorable-direction gate is:

```go
price >= lowerSellBoundary

```

Report:

- Boundary formula.
- Boundary value at the supplied snapshot.
- Observed price.
- Distance from the reference.
- Why the region is neither an exact-point overfit nor an unrestricted entry.

## Phase 6: Define continuation

Use the standard committed-reference continuation rule unless explicitly approved otherwise:

```go
BUY:  price <= lastCommittedProducerPrice * 0.998
SELL: price >= lastCommittedProducerPrice * 1.002

```

Continuation must:

- Be scoped to the same producer and side.
- Use the last confirmed committed fill.
- Preserve the producer’s non-price signal qualification.
- Replace only the producer’s first-entry price-admission gate.
- Use pending single-flight protection.
- Remain blocked during unresolved exchange reconciliation.

## Phase 7: Apply the producer standard

If behavior is not explicitly specified, copy Case11/common producer behavior.

The producer must use shared mechanisms for:

- Independent per-tick evaluation.
- Immutable evaluator inputs.
- Pending single-flight.
- Continuation references.
- Tier economics.
- Resource priority.
- Resource requests.
- Full and partial allocation.
- Allocation rejection.
- Refund participation.
- Order construction.
- Maker/post-only or currently standardized ordinary execution.
- Exchange acceptance.
- Pending registration.
- Fill and partial-fill handling.
- Cancellation.
- Reconciliation.
- Commit and lot creation.
- State transitions.
- Durable history.
- Durable performance economics.
- Complete BOT OPS observability.

Do not create producer-specific duplicates of these mechanisms.

Exceptions require explicit user agreement.

## Phase 8: Observability requirements

The standard lifecycle is:

```text
evaluation
→ decision / decision_failed
→ produced
→ allocation_requested
→ allocation_approved / allocation_partial / allocation_rejected
→ submission_started
→ exchange_accepted
→ pending
→ filled / cancel_requested / entry_failed / reconciliation
→ committed / commit_failed / cleanup_cancelled / cleanup_cancel_failed

```

Every persisted lifecycle event must include:

```text
stage-specific information
|stage.elapsed_ms=N
|hotpath.elapsed_ms=N

```

The producer’s reason must expose all importantly material signal inputs, including:

- Signal kind.
- AI direction and confidence.
- Thresholds.
- Regime.
- Relevant MACD values and comparisons.
- Relevant EMA values and patterns.
- Price reference.
- Price boundary.
- Observed price.
- Pending count.
- Continuation mode and reference.
- Tier and multiplier.
- Priority.
- Entry-gate result.

A valid decision must remain observable even if allocation is later partial or rejected.

Rejected gate evaluations may use lightweight trace telemetry rather than durable per-tick attempts, but every created producer lifecycle and outcome must be durable and inspectable.

## Phase 9: BOT OPS standard

BOT OPS must:

- Discover producers dynamically from durable History and Economics.
- Display a producer even before it has filled.
- Use the raw producer identifier as a fallback label.
- Allow optional friendly-label registration.
- Display all lifecycle stages, reasons, failures, timings and economics.
- Provide full producer-detail inspection.
- Avoid a fixed allowlist that requires another UI mutation for every producer.

A future producer must become visible through registration and persisted activity without requiring a new hard-coded card.

## Phase 10: Present the pre-implementation proposal

Before changing code, present:

1. Snapshot interpretation.
2. Existing-producer comparison.
3. Proposed name and label.
4. Side and market thesis.
5. Complete pass expression.
6. Passing-value table.
7. Price-area formula and calculated boundaries.
8. Conditions deliberately excluded.
9. Tier and next priority.
10. Continuation behavior.
11. Exact reason fields.
12. Files expected to change.
13. Any decisions that genuinely require user approval.

Do not begin implementation until the user approves the contract.

## Phase 11: Implement after approval

Use the uploaded current files as the authoritative behavioral base.

Make the smallest complete end-to-end mutation.

A producer evaluator should follow this general contract:

```go
func apply<Name>Producer(
    d *EntryDecision,
    ai AIResult,
    macd MACDResult,
    ema EMAPatternResult,
    pyramid PyramidResult,
    price float64,
    regime MarketRegime,
    pendingCounts PendingProducerCounts,
    continuationRefs ProducerContinuationReferences,
) bool

```

The evaluator must:

- Return `false` for a nil decision.
- Avoid shared-state mutation.
- Calculate first-entry and continuation gates.
- Apply pending single-flight.
- Set the signal, producer, policy and diagnostics.
- Call shared producer economics.
- Populate a complete producer reason.
- Return `true` only when every required condition passes.

Register the producer in:

- Producer identity.
- Tier mapping.
- Priority mapping.
- Entry policy.
- Independent producer collection.
- Any genuinely exhaustive producer switch.
- BOT OPS friendly labels, if desired.

Do not modify generic coordinator, Trader, observability or frontend files merely to mention the new producer when their handling is already dynamic.

## Phase 12: Validate

Perform or provide commands for:

```bash
gofmt -w <changed-go-files>
gopls check <changed-go-files>
go test ./...
go build ./...

```

For BOT OPS:

```bash
python3 -m py_compile app.py gate_state_plot.py

```

Also verify:

- The supplied snapshot replays as a passing decision.
- One failed-gate test for every condition.
- Pending duplicate suppression.
- Continuation threshold behavior.
- Full, partial and rejected allocation.
- Refund attachment.
- Submission rejection.
- Exchange uncertainty and reconciliation.
- Commit and lot creation.
- Restart persistence.
- BOT OPS summary and producer details.
- Dynamic discovery of an unfamiliar producer identifier.

Report any validation that could not be run. Never claim compilation or runtime verification that was not performed.

## Completion rule

A producer is complete only when its decision, allocation outcome, exchange lifecycle, persistence, economics, reasons and timing are observable in BOT OPS.

Return the changed files only after completing all agreed and anticipated end-to-end changes.