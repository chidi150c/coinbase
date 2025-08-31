1) what do you mean by "in the earlier EXIT loop"
2) "the fix you want is to ignore model-driven SELLs entirely" does it mean AI is making wrong SELL decision? and what else will the model be used for if ignored here? 
3) How is SL affecting the trade with the current STOP_LOSS env value?
4) if "exits will come from TAKE_PROFIT (or manual/circuit-breaker)," why did I not see which order was circuit breaker which order was exit or long? was the webhook alerted ?
5) "lot.Side == SideBuy && (price <= lot.Stop || price >= lot.Take)" this condition to BUY and populate t.Lots will never be true since STOP_LOSS_PCT=1000.0. So we need to just depend on PYRAMID_MIN_ADVERSE_PCT
==============================
may be to do:
Enforce strict long-only while long (SELL signals ignored whenever long).
Minimal patch (adds a second guard; pyramiding BUYs still work; exits stay via TP/SL):

// Block SELL any time LongOnly is true (even when already long).
if d.Signal == Sell && t.cfg.LongOnly {
    t.mu.Unlock()
    return "HOLD", nil
}


(You can keep the existing “FLAT (long-only)” line for the flat case if you want the old log text there; for the in-position case “HOLD” is fine.)
=====
another version of above:
✅ Correct design (if you want only TP/SL exits):
Keep this check as-is (it’s correct for TP/SL exits).
But modify the main step() to ignore model SELLs while long:

// Prevent model-driven SELL while holding BUY lots
if len(t.lots) > 0 && d.Signal == Sell {
    t.mu.Unlock()
    return "HOLD", nil
}


That way:

Entries = BUY (and pyramid adds if allowed).

Exits = only when TP or SL triggers via that code you quoted.

No premature SELLs from the AI.

Do you want me to generate the exact step() patch to enforce “ignore SELL signals when long, use only TP/SL closes”? That would lock in the behavior you want.
====================================
When closing a position: in backtest simulate charging exchange fees

if !t.cfg.DryRun {
    // real broker call
} else {
    // just simulate PnL and adjust equity
}