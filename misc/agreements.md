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

===================================================================================
latest below: 
===================================================================================

Consolidated implementation agreement list
Completed and confirmed
BOT OPS Unfilled Order-ID visibility
Unfilled rows use pending.OrderID.
Filled and Exited mappings remain unchanged.
Pending-only attempts remain visible.
Confirmed using order 66219499081.
Case3A deferred-retry decision lifecycle
Every retry creates a proper decision event before produced.
It records obligation ID, original decision, source order, recovery method/amount, retry cause, waiting exit, and target price.
The same attempt identity continues through later stages.
Plural durable Case3A obligations
Multiple simultaneous obligations are supported.
Case3AObligations and PendingReplacementRetries are maps.
Obligations survive restart and unsuccessful execution attempts.
Zero-fill cancellation, rejection, or expiry returns an obligation to waiting_for_target.
Partial fills preserve the remaining obligation.
Confirmed completed obligations are removed immediately.
Reconciliation uncertainty blocks duplicate resurrection.
Build and tests passed before deployment under commit 6ab3bf0.
Case3A obligation overlay rule
Existing initial Mode A and Mode B behavior remains intact.
Obligations do not obstruct initial Case3A processing.
The obligation takes over only at a genuine Case3A failure point.
A completely successful initial Case3A replacement completes/removes its obligation instead of activating resurrection.
Resurrected Case3A execution policy
Preserve the original economic target.
Resurrection waits for the price to satisfy the fee-and-slippage-adjusted target.
SELL uses executable bid; BUY uses executable ask.
Resurrected attempts use a dedicated market/taker route—not post-only.
RecoveryNetUSD, recovery lineage, and partial-recovery accounting are carried forward.
This does not globally change ordinary producer execution or generic repricing.
Exit fan-out correctness
Acted represents an exit genuinely started or filled.
succeeded increments only for Acted=true.
Maker-exit submission errors are propagated.
pending_exit.start_failed records entry ID, side, limit, size, and error.
This exposed the insufficient-balance retry loop rather than solving its funding shortage.
Producer-history submission batching
Pre-submission lifecycle events remain in memory during the batch.
History is flushed once after the coordinator submission batch.
submission_started and exchange_accepted are explicit events.
The existing pending/broker timing remains available.
Producer economics and lifecycle meaning remain unchanged.
Hot-path and exit-scan instrumentation
Lifecycle stages carry their originating hotStart.
Required reason suffix:
stage-specific information|stage.elapsed_ms=N|hotpath.elapsed_ms=N
Original decision reason remains where it was already included.
hotpath.producer.stage_timing and internal exit-scan timing were added.
The observed recurring ~246 ms was identified as the synchronous Binance maker-exit request/response, not exit selection or Refund throttling.
ResourceManager phase one
Transient pre-submission producer reservations moved from Trader into an actor-style ResourceManager.
Resource allocation calculation runs through that manager.
Submission behavior remained synchronous.
gofmt, gopls, tests, and build passed.
Implemented/generated but awaiting your validation
ResourceManager phase two
step() now copies immutable balance, lot, and pending-entry exposure.
ResourceManager calculates:
reserved quote;
reserved base;
spare quote/base;
pending-entry exposure;
transient pre-submission reservations;
current and available lot slots;
balance freshness/validity.
Refund throttling and submission ordering remain unchanged.
Updated resource_manager.go and step.go were generated, but your local gofmt, gopls, test, and build results have not yet been received.
General Refund-obligation implementation
Refund obligations are not limited to exits or Case3A.
Both entry-origin and exit-origin resource shortages can create them.
Each obligation has a stable identity, source operation/order, shortage side, service side, requested, reserved, filled, and remaining amounts.
Ordinary producer core allocation comes first; Refund attachment has the lowest priority.
Refund is attached through the existing coordinator allocation result.
Case3A may carry a Refund attachment when spare remains available.
A confirmed Refund fill completes and retires that Refund obligation.
Restored resources become ordinary spare and are not protected afterward.
Exit-origin Refund retains the agreed approximately 30-second retry throttle.
This mutation was supplied, but final deployed behavior and BOT OPS output have not been confirmed.
Remaining scheduled implementation
Correct partial-fill ProfitGateUSD apportionment
RecoveryNetUSD is already proportional to the committed Case3A core fill.
ProfitGateUSD must also be restored proportionally.
A partial allocation/fill must not receive the complete original profit gate.
The unfilled portion retains its corresponding remaining gate in the obligation.
Asynchronous exit submission overlap
Move only the slow exit submission portion off the main hot path.
Exit selection and resource reservation occur first.
Producer analysis may proceed while Binance processes the exit request.
A channel/fan-in is compulsory before building the authoritative entry-resource snapshot or submitting any entry.
Exit results must update resource ownership before entry allocation.
step() must not return merely because the asynchronous request was launched.
Refund retry throttling must remain unchanged.
Complete ResourceManager separation
Finish moving authoritative funding/resource coordination out of Trader.
Keep Trader as the owner of trading state, while ResourceManager owns resource arithmetic and transient reservations.
Remove or retire the unused legacy buildResourceSnapshotLocked() path after the new build passes.
Avoid maintaining two competing snapshot authorities.
Resource allocation may be off the main hot path, but no entry may race unresolved exit consumption.
Case13B symmetry
Make Case13B the BUY/SELL mirror of Case13A.
Mirror trigger, continuation, gate, sizing, lifecycle, and accounting behavior.
Preserve side-specific price and resource semantics.
This remains unimplemented.
Refund observability
Include Refund information prominently in producer reasons:
refund_obligation_id
refund_origin
refund_source_order_id
refund_shortage_side
refund_service_side
refund_requested_usd
refund_reserved_usd
refund_filled_usd
refund_remaining_usd
BOT OPS should make requested, allocated, filled, outstanding, and retired Refund obligations easy to identify.
Do not infer Refund involvement merely from order size.
Initial-entry latency optimization
Continue measuring:
decision creation;
price selection;
resource snapshot/allocation;
lifecycle persistence;
submission start;
exchange response;
pending registration;
final persistence.
Determine whether delay actually caused the favorable initial price to be missed.
Investigate the observed 6.3–6.7-second queued/throttled submissions separately from Case3A obligations.
Do not treat cumulative hotpath.after_decision as exchange latency.
Producer-stage reason completeness
Verify every actual lifecycle-writing path supplies:
meaningful stage-specific information;
stage.elapsed_ms;
hotpath.elapsed_ms.
This includes asynchronous filled, cancellation, cleanup, commit, failure, and reconciliation paths.
Preserve the original producer reason wherever it is already expected.
Producer-history pruning protection
Producer history remains a display/audit store—not the authoritative obligation store.
Consider explicitly protecting live pending attempts from the 24-hour/500-attempt pruning rules.
Durable Case3A and Refund obligations must remain independent of history pruning.
Generic repricing review—deferred
Repricing remains disabled.
Do not enable adverse price chasing.
Any future implementation must preserve producer economics, required profit, fees, and favorable trade points.
The dedicated Case3A resurrection market route does not activate generic repricing.
