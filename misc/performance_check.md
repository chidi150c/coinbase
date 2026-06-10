

1) trade attempts

awk -v start="$(date -u -d '6 hours ago' '+%Y/%m/%d %H:%M:%S')" '
$0 ~ /\[DEBUG\]/ &&
$0 ~ /Decision=/ {

    ts = $3 " " $4
    gsub(/\|/, "", ts)

    if (ts >= start) {
        n++

        if ($0 ~ /Decision=BUY/)  buy++
        if ($0 ~ /Decision=SELL/) sell++
        if ($0 ~ /Decision=FLAT/) flat++

        if ($0 ~ /logicOpinion=BUY/)  logicBuy++
        if ($0 ~ /logicOpinion=SELL/) logicSell++

        if ($0 ~ /final=BUY/)  finalBuy++
        if ($0 ~ /final=SELL/) finalSell++
        if ($0 ~ /final=FLAT/) finalFlat++

        if ($0 ~ /ai_FLAT_logicOpinion=BUY_allowed/) aiFlatLogicBuy++
        if ($0 ~ /ai_FLAT_logicOpinion=SELL_allowed/) aiFlatLogicSell++

        if ($0 ~ /logic_disagreement/) disagreement++
    }
}
END {
    print "=== LAST 6 HOURS TRADE ATTEMPTS ==="
    print "rows=" n
    print ""
    print "Decision BUY =", buy
    print "Decision SELL=", sell
    print "Decision FLAT=", flat
    print ""
    print "logic BUY    =", logicBuy
    print "logic SELL   =", logicSell
    print ""
    print "final BUY    =", finalBuy
    print "final SELL   =", finalSell
    print "final FLAT   =", finalFlat
    print ""
    print "AI_FLAT+BUY  =", aiFlatLogicBuy
    print "AI_FLAT+SELL =", aiFlatLogicSell
    print ""
    print "disagreement =", disagreement
}
' /opt/coinbase/logs/audit/binance_audit.log

=========================================================================

2) what blocked them

awk -v start="$(date -u -d '3 hours ago' '+%Y/%m/%d %H:%M:%S')" '
{
 ts=$3" "$4; gsub(/\|/,"",ts)
 if (ts < start) next

 if ($0 ~ /TRACE pyramid.block.buy/)  pyrBuy++
 if ($0 ~ /TRACE pyramid.block.sell/) pyrSell++
 if ($0 ~ /TRACE lotcap/) lotcap++
 if ($0 ~ /TRACE sizing.confidence/) sizing++
 if ($0 ~ /ORDER/) order++
 if ($0 ~ /HOLD/) hold++
}
END {
 print "pyramid.block.buy =", pyrBuy
 print "pyramid.block.sell=", pyrSell
 print "lotcap           =", lotcap
 print "sizing reached   =", sizing
 print "orders           =", order
 print "HOLD lines       =", hold
}
' /opt/coinbase/logs/audit/binance_audit.log

==============================================================

3) Next check should be why pyramid blocked:

grep -E "TRACE pyramid.block.(buy|sell)" /opt/coinbase/logs/audit/binance_audit.log | tail -30