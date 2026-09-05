Agreed work and constraints — updated
1. BOT OPS Unfilled Order ID visibility — completed and deployment confirmed
Unfilled now reads pending.OrderID instead of decision.OrderID.
Pending-only attempts without a decision event are included through a pending-event fallback.
Filled and Exited mappings remain untouched.
Only gate_state_plot.py changed.
Tested against order 66219499081; your deployed screenshot confirms its ID appears in the summary row. The count increased from 498 to 499.
This display fallback does not resolve the missing Go lifecycle decision event.
2. Case3A retries must record a proper decision event
Retain this requirement; disregard the temporary suggestion to omit it.
Trace newProducerIntentLifecycle() and record a decision before produced, using the retry’s existing attempt identity.
Explain the source entry/exit, recovery method, recovery amount, retry circumstances, and price condition.
Distinguish the persistent replacement obligation from each individual execution attempt.
3. Case3A replacement obligations must survive cancellation
Once the originating exit commits, an unfilled cancellation must not abandon its replacement requirement.
Current uploaded code disables PendingReplacementRetry.Enabled when submission succeeds—not when the replacement fills.
The unfilled terminal-result path removes the pending entry without restoring that retry request.
Case3A participates in the shared pendingEntries map, but PendingReplacementRetry is currently a single struct, not a collection of independent obligations.
Preserve outstanding obligations durably in trader state, independently of producer-history retention.
Track source-exit identity, original target, required replacement, filled/remaining quantity, active order, and completion state.
Allow only one live execution attempt per obligation.
Reconcile cancellation and possible fills before retrying. Retry only the remaining requirement.
Persist obligations across restarts; finish them only after confirmed execution and correct accounting.
4. Price-controlled Case3A retries using market execution
Preserve the original replacement target; do not chase an adverse price.
For SELL, retry when the executable bid is sufficiently above the original target.
For BUY, retry when the executable ask is sufficiently below it.
Use a market order once the price advantage covers applicable fees and an allowed slippage buffer while preserving recovery/profit economics.
Avoid counting fees twice where already included.
This applies to Case3A obligation retries—not a global switch to market execution.
Market execution cannot guarantee the observed trigger price.
Exact buffer calculation and parameters still require implementation review.
5. Initial-entry latency — investigate separately
hotpath.after_decision=432 ms is cumulative step timing, not exchange submission latency.
This Case3A order was already accepted before that checkpoint.
Lifecycle creation to pending recording took approximately 247 ms, including local processing and persistence.
Instrument price selection, submission, response, registration, and persistence separately.
Investigate initial-limit reuse and post-only rejection.
Earlier failed Case3A attempts appear from at least 19:57:31 UTC; the previously discussed nine seconds covered only the final portion. Verify their reasons and lineage before treating them as one retry sequence.
Fresh BBO pricing and bounded immediate retries remain proposals for initial placement, not implemented changes.
6. Repricing must preserve favorable economics
Do not simply enable adverse price chasing.
Verify actual configuration semantics and guard behavior.
Preserve producer-specific fees, required profit, and Case3A recovery requirements.
Keep generic repricing review separate from the agreed Case3A market-retry policy.
No repricing changes have been implemented.
7. Refund sizing and BOT OPS visibility — investigate
Determine whether refund balances or historical shortfalls contributed to this order’s size.
Separate refund requested, applied, consumed, and outstanding from Case3A recovery sizing.
Make refund participation conspicuous in producer reasons and BOT OPS.
A large order alone is not proof of refund involvement.
Verified producer-history pruning
Retention is 24 hours, plus a 500-attempt cap per producer.
Unfilled/cancelled attempts age from CreatedAt, not cancellation time.
The count cap can delete eligible attempts before 24 hours, oldest first.
Pruning runs during producer-history saves and startup loading.
Entire attempts are removed; individual pending events are not separately removed.
Economics and error counts are folded into durable aggregates before deletion.
Live unfilled pending attempts are not explicitly protected by either pruning rule.
Unresolved fills and live committed exposure are protected.
The plotter’s separate 48-hour display cutoff does not delete stored history.
Therefore, producer history must not be the authoritative store for unfinished replacement obligations.
Confirmed incident facts
Field	Value
Order	66219499081
Decision ID	Case3AReplacement_20260903T200009M717
Producer / side	Case3AReplacement / SELL
Source entry order	66219160349
Method	RecoveryByProfitTarget
Recovery amount	$3.501615
Limit	81731.15
Quantity	0.00712 BTC
Value	$581.925788
Pending recorded	September 3, 20:00:09.963 UTC
Cancel requested	20:20:10.052 UTC, poller_final_cancel
Cleanup cancelled	20:20:10.320 UTC
Recorded stages: produced, pending, cancel_requested, cleanup_cancelled.
No decision, filled, or committed event.
Value was approximately 77% of contemporaneous $759.82 equity.
Persistence of the attempt is confirmed; BOT OPS now displays it.
The apparent summary/history timestamp discrepancy remains unverified.
Earlier completed agreements
NormalLegacy regime reset is producer-owned and independent of market/post-only execution.
Shared commitEntryFill() delegation to entryPolicyForSource() is acceptable.
Startup lot-cap consolidation respects Equity’s historical bypass.
One [DEBUG] Total Lots message per evaluated tick, including FLAT/no-candidate ticks, without selected_* duplication.
Working approach

Confirm causes against current deployed code before mutation. Preserve unrelated behavior, especially Filled/Exited rendering. Apart from the deployed Unfilled display fix, the investigations and Go execution/lifecycle changes above remain outstanding.