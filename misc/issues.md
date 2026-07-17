I would make the completion criteria objective and measurable. A case should only be marked Completed when both the implementation and its intended behavior have been verified.

Case 1 — Entry Decision Quality

Purpose: Improve trade selection by ensuring BUY/SELL entries are opened only when AI, confidence, thresholds, technical indicators, and market regime provide sufficient evidence. Focuses on maximizing long-term expectancy rather than trade frequency.

Completion Criteria
✅ Entry decisions are fully explainable from logs (AI, Logic, confidence, thresholds, MACD, EMA, MarketRegime).
✅ BUY and SELL opportunities occur when expected and are not unintentionally suppressed.
✅ Historical and live analysis confirms improved trade expectancy over the previous implementation.
✅ No significant increase in false entries or unnecessary trades.
✅ No known regressions in entry behavior remain.

Case 2 — Maker Execution & Hot Path

Purpose: Optimize the path from market observation to order placement, minimizing latency and ensuring maker orders are submitted, repriced, and filled as efficiently as possible.

Case 2A — Hot Path Optimization

Reduce all unnecessary delays and blocking operations between price acquisition, decision making, and order submission.

Case 2B — Maker Repricing

Analyze and improve post-only repricing decisions so valid maker orders remain competitive without sacrificing maker execution.

Completion Criteria
✅ No unnecessary blocking operations remain in the decision-to-order path.
✅ Hot-path latency is measured and remains within the target.
✅ Every skipped reprice has an explainable and expected reason.
✅ Maker order fill performance has been validated under live trading.
✅ No unnecessary execution delays remain between decision and order placement.
Case 3 — SELL Loss Management

Purpose: Reduce repeated SELL losses and intelligently recover from unavoidable loss-stop exits.

Case 3A — SELL Loss Protection

Prevent repeated SELL entries below the previous SELL loss-stop BUY exit price while the market is in an UP regime.

Case 3B — SELL Loss Recovery

Automatically recover realized SELL losses through adaptive replacement sizing, profit-target adjustment, and persistent retry mechanisms.

Completion Criteria
✅ SELL loss protection correctly blocks prohibited SELL entries while allowing valid SELL opportunities.
✅ Recovery sizing calculations match the intended formulas.
✅ Recovery modes (A and B) function correctly under live conditions.
✅ Retry logic survives failures and bot restarts.
✅ RecoveryDebtUSD increases and decreases correctly.
✅ Live trading confirms recovery improves overall strategy performance without introducing excessive exposure.
Case 4 — Profit-Giveback Protection

Purpose: Prevent trades that have already reached their profit gate from giving back gains and eventually turning into losing trades by tracking profit peaks and protecting accumulated profit.

Completion Criteria
✅ Protection arms only after the profit gate is reached.
✅ ProfitPeakUSD is tracked correctly.
✅ Protected floor is calculated correctly.
✅ Protection exits occur only under the intended conditions.
✅ All protection exits are correctly classified as L2_PROFIT_PROTECTION.
✅ Protection significantly reduces profitable trades turning into losses.
✅ Any case4.protection_missed events are understood, explainable, and within acceptable limits.
Case 5 — Concurrent Decision Engine (Fan-Out / Fan-In Refactor)

Purpose: Improve scalability and responsiveness by evaluating independent decision components concurrently (AI, MACD, EMA, etc.) using a fan-out/fan-in architecture while preserving legacy behavior, observability, and correctness through verification.

Completion Criteria
✅ Fan-out/fan-in execution is completed for every evaluation cycle.
✅ No goroutine leaks or abandoned evaluations occur.
✅ Decision results are behaviorally equivalent to the legacy implementation.
✅ Legacy logging and observability are fully restored.
✅ Long-duration live testing confirms stable operation without concurrency-related regressions.
✅ The new architecture demonstrably reduces decision latency or improves scalability without changing strategy behavior.
Case 6 — Duplicate Balance Refresh

Purpose: Eliminate unnecessary duplicate broker/bridge balance retrieval caused by both the background balance refresher and the post-step() live-equity refresh, reducing exchange/API traffic while maintaining accurate equity information.

Completion Criteria
✅ Duplicate balance requests are eliminated or explicitly justified.
✅ Balance information remains accurate throughout trading.
✅ Equity calculations remain correct.
✅ No increase in stale-balance decisions is observed.
✅ Exchange/bridge API traffic is reduced without affecting trading correctness.
✅ Live testing confirms no regressions after consolidation.

These completion criteria have a common philosophy: a case is complete only when (1) the implementation is finished, (2) it has been verified under live trading, and (3) it demonstrably achieves its intended objective without introducing regressions. This gives you a clear definition of "done" for every case.