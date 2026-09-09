Case3A decision
→ durable obligation created as waiting_for_exit
→ evaluate Mode A


MODE A — RecoveryByPositionSize

if sufficient spare base exists:
    → submit the initial Mode A replacement before submitting the source exit

    if Mode A submission is accepted:
        → obligation status = ready
        → record the active replacement order
        → allow the source BUY exit to proceed

        if Mode A replacement fully fills and commits:
            → complete and delete obligation

        if Mode A replacement partially fills:
            → reduce remaining base and recovery proportionally
            → preserve the remaining obligation

        if Mode A replacement is cancelled/rejected/expired:
            if source position still exists:
                → obligation status = waiting_for_exit
            otherwise:
                → obligation status = active

        if Mode A execution status is uncertain:
            → obligation status = reconcile
            → do not submit another replacement or release its resources

    if initial Mode A submission fails definitively:
        → do not submit the source exit during that attempt
        → preserve the complete obligation
        → obligation remains waiting_for_exit
        → record the Mode A failure and retry classification
        → permit a later Mode A attempt while the source position still exists

        if a later Mode A attempt succeeds:
            → obligation status = ready
            → allow the source exit to proceed

        if policy eventually permits the source exit without Mode A:
            → migrate the obligation to Mode B
            → continue through the Mode B path below


if sufficient spare base does not exist:
    → record case3a_mode_a_blocked
    → select Mode B — RecoveryByProfitTarget


MODE B — RecoveryByProfitTarget

→ start the source BUY exit
→ submit the initial Mode B replacement

if initial Mode B replacement is accepted:
    → obligation status = ready
    → wait for the replacement-order result

    if fully filled and committed:
        → complete and delete obligation

    if partially filled:
        → reduce the remaining obligation

    if cancelled/rejected/expired with remaining quantity:
        if source position still exists:
            → obligation status = waiting_for_exit
        otherwise:
            → obligation status = active

    if execution status is uncertain:
        → obligation status = reconcile


if initial Mode B submission fails definitively:
    → create PendingReplacementRetries entry
    → preserve the complete obligation
    → obligation remains waiting_for_exit


MODE B DEFERRED RETRY

→ later tick: source BUY exit fills and commits
→ locate the obligation’s PendingReplacementRetries entry
→ perform exactly one deferred Mode B retry
→ use the base returned by the completed source exit

if the deferred Mode B retry is accepted:
    → obligation status = ready
    → remove the PendingReplacementRetries entry
    → wait for the replacement-order result

    if fully filled and committed:
        → complete and delete obligation

    if partially filled:
        → reduce remaining base and recovery proportionally
        → remaining obligation becomes active

    if cancelled/rejected/expired with remaining quantity:
        → obligation becomes active

    if execution status is uncertain:
        → obligation status = reconcile


if the deferred Mode B retry submission fails definitively:
    → remove the PendingReplacementRetries entry
    → obligation becomes active


OBLIGATION RESURRECTION

→ active obligation checks the original economic target adjusted for:
    taker fee + configured slippage allowance

if adjusted target is not satisfied:
    → obligation status = waiting_for_target

if adjusted target is satisfied:
    → submit through the dedicated market-resurrection path

    if fully filled and committed:
        → complete and delete obligation

    if partially filled:
        → reduce remaining base and recovery proportionally
        → return the remainder to waiting_for_target

    if definitely rejected or zero-filled:
        → return to waiting_for_target
        → permit another later resurrection attempt

    if execution status is uncertain:
        → obligation status = reconcile