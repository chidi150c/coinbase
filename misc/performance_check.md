

1) trade attempts

awk -v start="$(date -u -d '1 hours ago' '+%Y/%m/%d %H:%M:%S')" '
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
) Run this first to identify the latest trade/order and the 60 lines before it:

grep -nE "LIVE ORDER|FILLED|filled|ORDER|Decision=(BUY|SELL)|pyramid:|OPEN-PENDING" \
/opt/coinbase/logs/audit/binance_audit.log | tail -120
======================================================================================
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

=============================================================
Case 2.2A — AI_FLAT but pUp is outside the model's avg band
Case 2.2B — AI_FLAT but pUp is between model's up/down averages

) Checking Case 2.2A vs Case 2.2B count:

grep "aiRaw=FLAT" /opt/coinbase/logs/audit/binance_audit.log | \
awk -v cutoff="$(date -u -d '24 hours ago' '+%Y/%m/%d %H:%M:%S')" '
{
  ts = $3 " " $4
  if (ts < cutoff) next

  if (match($0,/pUp=([0-9.]+)/,p) &&
      match($0,/modelUpAvg=([0-9.]+)/,u) &&
      match($0,/modelDownAvg=([0-9.]+)/,d)) {
    pup=p[1]+0; up=u[1]+0; down=d[1]+0
    total++
    if (pup >= up) buyBias++
    else if (pup <= down) sellBias++
    else holdBand++
  }
}
END {
  print "CASE_2_2_TOTAL =", total+0
  print "CASE_2_2A_BUY_BIAS =", buyBias+0
  print "CASE_2_2A_SELL_BIAS =", sellBias+0
  print "CASE_2_2B_HOLD_BAND =", holdBand+0
}'

=========================================================

Now debug why Case 2.2A still gives no LOGIC_BUY/SELL:

grep "aiRaw=FLAT" /opt/coinbase/logs/audit/binance_audit.log | \
awk -v cutoff="$(date -u -d '24 hours ago' '+%Y/%m/%d %H:%M:%S')" '
{
  ts=$3" "$4
  if (ts < cutoff) next
  if ($0 !~ /route=FLAT/) next

  total++
  if ($0 ~ /macdMomentumUp=true/) mup++
  if ($0 ~ /macdMomentumDown=true/) mdn++
  if ($0 ~ /emaBuyPattern=true/) ebuy++
  if ($0 ~ /emaSellPattern=true/) esell++
  if ($0 ~ /macdStrongNegative=true/) sneg++
  if ($0 ~ /macdStrongPositive=true/) spos++

  if ($0 ~ /macdMomentumUp=true/ && $0 ~ /emaBuyPattern=true/) buy2++
  if ($0 ~ /macdMomentumDown=true/ && $0 ~ /emaSellPattern=true/) sell2++
  if ($0 ~ /macdMomentumUp=true/ && $0 ~ /emaBuyPattern=true/ && $0 ~ /macdStrongNegative=true/) buyFull++
  if ($0 ~ /macdMomentumDown=true/ && $0 ~ /emaSellPattern=true/ && $0 ~ /macdStrongPositive=true/) sellFull++
}
END {
  print "CASE_2_2A_ROUTE_FLAT =", total+0
  print "macdMomentumUp =", mup+0
  print "macdMomentumDown =", mdn+0
  print "emaBuyPattern =", ebuy+0
  print "emaSellPattern =", esell+0
  print "macdStrongNegative =", sneg+0
  print "macdStrongPositive =", spos+0
  print "BUY momentum+EMA =", buy2+0
  print "SELL momentum+EMA =", sell2+0
  print "BUY full =", buyFull+0
  print "SELL full =", sellFull+0
}'

==========================================================================
regime exploration:

grep "aiRaw=FLAT" /opt/coinbase/logs/audit/binance_audit.log | \
awk -v cutoff="$(date -u -d '24 hours ago' '+%Y/%m/%d %H:%M:%S')" '
{
  ts=$3" "$4
  if (ts < cutoff) next
  hour=substr(ts,1,13)

  if ($0 ~ /logicOpinion=BUY/) lb[hour]++
  if ($0 ~ /logicOpinion=SELL/) ls[hour]++
  if ($0 ~ /logicOpinion=FLAT/) lf[hour]++

  if ($0 ~ /route=FLAT/ &&
      $0 ~ /macdMomentumUp=true/ &&
      $0 ~ /emaBuyPattern=true/ &&
      $0 ~ /macdStrongNegative=true/) bf[hour]++

  if ($0 ~ /route=FLAT/ &&
      $0 ~ /macdMomentumDown=true/ &&
      $0 ~ /emaSellPattern=true/ &&
      $0 ~ /macdStrongPositive=true/) sf[hour]++
}
END {
  for (h in lf)
    print h, "LOGIC_BUY=" lb[h]+0, "LOGIC_SELL=" ls[h]+0, "LOGIC_FLAT=" lf[h]+0, "BUY_FULL=" bf[h]+0, "SELL_FULL=" sf[h]+0
}' | sort

----------- output --------------------------
2026/06/11 05 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=2584 BUY_FULL=0 SELL_FULL=0
2026/06/11 06 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=1170 BUY_FULL=0 SELL_FULL=0
2026/06/11 07 LOGIC_BUY=308 LOGIC_SELL=0 LOGIC_FLAT=2370 BUY_FULL=308 SELL_FULL=0
2026/06/11 08 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=887 BUY_FULL=0 SELL_FULL=0
2026/06/11 09 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=2359 BUY_FULL=0 SELL_FULL=0
2026/06/11 10 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=1708 BUY_FULL=0 SELL_FULL=0
2026/06/11 11 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=102 BUY_FULL=0 SELL_FULL=0
2026/06/11 12 LOGIC_BUY=52 LOGIC_SELL=0 LOGIC_FLAT=1980 BUY_FULL=52 SELL_FULL=0
2026/06/11 13 LOGIC_BUY=0 LOGIC_SELL=154 LOGIC_FLAT=2415 BUY_FULL=0 SELL_FULL=154
2026/06/11 14 LOGIC_BUY=310 LOGIC_SELL=0 LOGIC_FLAT=2506 BUY_FULL=310 SELL_FULL=0
2026/06/11 15 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=1028 BUY_FULL=0 SELL_FULL=0
2026/06/11 16 LOGIC_BUY=0 LOGIC_SELL=0 LOGIC_FLAT=680 BUY_FULL=0 SELL_FULL=0

============================================================================

Now correlate MODEL_FIT regime vs logic regime by hour.

grep "\[MODEL_FIT\]" /opt/coinbase/logs/audit/binance_audit.log | \
awk -v cutoff="$(date -u -d '24 hours ago' '+%Y/%m/%d %H:%M:%S')" '
{
  ts = $3 " " $4
  if (ts < cutoff) next

  hour = substr(ts,1,13)

  if (match($0,/acc=([0-9.]+)/,a)) acc[hour]=a[1]
  if (match($0,/precision=([0-9.]+)/,p)) prec[hour]=p[1]
  if (match($0,/recall=([0-9.]+)/,r)) rec[hour]=r[1]

  if (match($0,/avg_up=([0-9.]+)/,u)) up[hour]=u[1]
  if (match($0,/avg_down=([0-9.]+)/,d)) down[hour]=d[1]
  if (match($0,/separation=([0-9.]+)/,s)) sep[hour]=s[1]

  if (match($0,/buy_q75=([0-9.]+)/,b)) buyT[hour]=b[1]
  if (match($0,/sell_q25=([0-9.]+)/,e)) sellT[hour]=e[1]
}
END {
  for (h in acc)
    print h,
          "acc=" acc[h],
          "prec=" prec[h],
          "recall=" rec[h],
          "avgUp=" up[h],
          "avgDown=" down[h],
          "sep=" sep[h],
          "buyThr=" buyT[h],
          "sellThr=" sellT[h]
}' | sort
---------------------output: [
}' | sort
2026/06/11 05 acc=0.5923 prec=0.5737 recall=0.4796 avgUp=0.49303 avgDown=0.43860 sep=0.05443 buyThr=0.55782 sellThr=0.36835
2026/06/11 06 acc=0.5958 prec=0.5850 recall=0.4505 avgUp=0.48793 avgDown=0.44342 sep=0.04451 buyThr=0.53418 sellThr=0.39117
2026/06/11 07 acc=0.5913 prec=0.5762 recall=0.4583 avgUp=0.49010 avgDown=0.44261 sep=0.04749 buyThr=0.54450 sellThr=0.38565
2026/06/11 08 acc=0.5928 prec=0.5830 recall=0.4369 avgUp=0.48233 avgDown=0.43321 sep=0.04912 buyThr=0.53601 sellThr=0.37359
2026/06/11 09 acc=0.5954 prec=0.5799 recall=0.4713 avgUp=0.49374 avgDown=0.44378 sep=0.04996 buyThr=0.55015 sellThr=0.38254
2026/06/11 10 acc=0.5938 prec=0.5765 recall=0.4768 avgUp=0.49701 avgDown=0.44826 sep=0.04876 buyThr=0.55359 sellThr=0.38872
2026/06/11 11 acc=0.5938 prec=0.5781 recall=0.4680 avgUp=0.49412 avgDown=0.44617 sep=0.04796 buyThr=0.54742 sellThr=0.38783
2026/06/11 12 acc=0.5910 prec=0.5782 recall=0.4454 avgUp=0.48721 avgDown=0.43998 sep=0.04723 buyThr=0.53963 sellThr=0.38273
2026/06/11 13 acc=0.5933 prec=0.5773 recall=0.4680 avgUp=0.49479 avgDown=0.44311 sep=0.05168 buyThr=0.55365 sellThr=0.38106
2026/06/11 14 acc=0.5963 prec=0.5823 recall=0.4664 avgUp=0.49323 avgDown=0.44336 sep=0.04987 buyThr=0.54810 sellThr=0.38401
2026/06/11 15 acc=0.5938 prec=0.5746 recall=0.4870 avgUp=0.49798 avgDown=0.45143 sep=0.04654 buyThr=0.55238 sellThr=0.39459
2026/06/11 16 acc=0.5933 prec=0.5768 recall=0.4708 avgUp=0.49782 avgDown=0.44809 sep=0.04973 buyThr=0.55479 sellThr=0.38793
2026/06/11 17 acc=0.5929 prec=0.5831 recall=0.4373 avgUp=0.48565 avgDown=0.43649 sep=0.04917 buyThr=0.54124 sellThr=0.37671
chidi@localhost:~$
]
====================================================================

When aiRaw=FLAT and full 1m logic alignment occurs: BUY_FULL == LOGIC_BUY, SELL_FULL == LOGIC_SELL
did that produce: Decision=BUY / Decision=SELL:

grep "aiRaw=FLAT" /opt/coinbase/logs/audit/binance_audit.log | \
awk -v cutoff="$(date -u -d '24 hours ago' '+%Y/%m/%d %H:%M:%S')" '
{
  ts=$3" "$4
  if (ts < cutoff) next

  if ($0 ~ /logicOpinion=BUY/) {
    lb++
    if ($0 ~ /final=BUY/ || $0 ~ /Decision=BUY/) fb++
    else blockedBuy++
  }

  if ($0 ~ /logicOpinion=SELL/) {
    ls++
    if ($0 ~ /final=SELL/ || $0 ~ /Decision=SELL/) fs++
    else blockedSell++
  }
}
END {
  print "AI_FLAT_LOGIC_BUY =", lb+0
  print "AI_FLAT_FINAL_BUY =", fb+0
  print "AI_FLAT_LOGIC_BUY_NOT_FINAL =", blockedBuy+0
  print "AI_FLAT_LOGIC_SELL =", ls+0
  print "AI_FLAT_FINAL_SELL =", fs+0
  print "AI_FLAT_LOGIC_SELL_NOT_FINAL =", blockedSell+0
}'

---------- output --------------------
AI_FLAT_LOGIC_BUY = 670
AI_FLAT_FINAL_BUY = 670
AI_FLAT_LOGIC_BUY_NOT_FINAL = 0
AI_FLAT_LOGIC_SELL = 154
AI_FLAT_FINAL_SELL = 154
AI_FLAT_LOGIC_SELL_NOT_FINAL = 0


=================================================================================

Why did those 824 final BUY/SELL decisions not become trades?

Next check entry blockers:

grep -E "Decision=(BUY|SELL)|GATE1|GATE2|pyramid: blocked|confidence=|FUNDS_|OPEN-PENDING|LIVE ORDER|TRACE buy.gate|TRACE sell.gate|postonly.market_fallback.blocked" \
/opt/coinbase/logs/audit/binance_audit.log | tail -300

===================================================================

when blocker is pyramid gate:

grep -E "LATCH REBASE" /opt/coinbase/logs/audit/binance_audit.log | tail -100

------------output------------
bot_binance-1  | 2026/06/11 13:01:07 [DEBUG] LATCH REBASE BUY: ageHr=150.60 logic=SELL old_latched=61104.24 old_winLow=61104.24 new_latched=62600.00 new_winLow=62600.00 price=62959.34

grep "TRACE recent.window" /opt/coinbase/logs/audit/binance_audit.log | tail -1

-------------output-----------------
bot_binance-1  | 2026/06/11 18:51:35 TRACE recent.window high=63780.00 low=62353.30

============================================================

So recentLow shows that at a point price was below latched so why no trade:

grep -E "TRACE step.start|Decision=BUY|pyramid: blocked by last gate \(BUY\)" /opt/coinbase/logs/audit/binance_audit.log | \
awk '
/TRACE step.start/ {
  ts=$3" "$4
  if (match($0,/price=([0-9.]+)/,p)) price=p[1]
  if (match($0,/recentLow=([0-9.]+)/,l)) recentLow=l[1]
  if (match($0,/latchedGateBuy=([0-9.]+)/,b)) latch=b[1]
}
/Decision=BUY/ {
  print "BUY_DECISION_CONTEXT ts=" ts, "price=" price, "recentLow=" recentLow, "latchedBuy=" latch
}
/pyramid: blocked by last gate \(BUY\)/ {
  print $0
}
' | tail -100
===============================================================================

| Time UTC |    Price |     pUp | aiRaw | Route | Logic | Final | AI MACD line / hist / dHist / dSmooth | Logic MACD line / hist / dHist / dSmooth | EMA / Pattern raw materials                          |
| -------- | -------: | ------: | ----- | ----- | ----- | ----- | ------------------------------------- | ---------------------------------------- | ---------------------------------------------------- |
| 21:26    | 64248.17 | 0.44625 | FLAT  | FLAT  | FLAT  | FLAT  | 14.68 / -7.21 / -0.36 / -1.11         | -8.58 / -1.75 / 0.995 / 1.13             | emaBuy=true, momentumUp=true, strongNeg=false        |
| 21:28    | 64260.39 | 0.47124 | FLAT  | HOLD  | FLAT  | FLAT  | 15.66 / -6.43 / 0.42 / -0.72          | -6.43 / 0.49 / 1.32 / 1.12               | emaBuy=false, momentumUp=true                        |
| 21:30    | 64260.40 | 0.47146 | FLAT  | HOLD  | FLAT  | FLAT  | 14.55 / -6.03 / 0.40 / 0.41           | -4.21 / 1.89 / 0.53 / 0.70               | emaBuy=false, momentumUp=true                        |
| 21:32    | 64275.39 | 0.55453 | BUY   | BUY   | FLAT  | FLAT  | 15.75 / -5.07 / 1.36 / 0.89           | -0.53 / 3.82 / 0.65 / 0.97               | emaBuy=false, momentumUp=true                        |
| 21:34    | 64291.77 | 0.58600 | BUY   | BUY   | FLAT  | FLAT  | 17.06 / -4.03 / 2.40 / 1.41           | 3.02 / 5.09 / 1.02 / 0.63                | emaBuy=false, momentumUp=true                        |
| 21:36    | 64297.10 | 0.59186 | BUY   | BUY   | FLAT  | FLAT  | 18.38 / -2.16 / 1.87 / 2.13           | 5.72 / 5.30 / 0.66 / 0.11                | emaSell=true, momentumUp=true                        |
| 21:38    | 64317.45 | 0.59014 | BUY   | BUY   | FLAT  | FLAT  | 20.00 / -0.86 / 3.16 / 2.78           | 10.72 / 6.99 / 0.73 / 0.84               | no EMA pattern, momentumUp=true                      |
| 21:40    | 64326.93 | 0.60524 | BUY   | BUY   | FLAT  | FLAT  | 23.03 / 1.69 / 2.36 / 2.86            | 14.75 / 7.37 / 0.14 / 0.19               | no EMA pattern, momentumUp=true                      |
| 21:41    | 64629.86 | 0.75432 | BUY   | BUY   | FLAT  | FLAT  | 47.19 / 21.02 / 21.69 / 12.52         | 40.31 / 26.34 / 18.97 / 9.55             | no EMA pattern, momentumUp=true                      |
| 21:42    | 64724.01 | 0.76199 | BUY   | BUY   | FLAT  | FLAT  | 54.70 / 27.03 / 27.70 / 15.53         | 67.38 / 42.73 / 16.39 / 17.68            | no EMA pattern, momentumUp=true                      |
| 21:43    | 64615.09 | 0.74003 | BUY   | BUY   | FLAT  | FLAT  | 46.01 / 20.08 / 20.75 / 12.05         | 79.13 / 43.58 / 0.86 / 8.62              | no EMA pattern, momentumUp=true                      |
| 21:44    | 64580.30 | 0.73032 | BUY   | BUY   | FLAT  | FLAT  | 43.24 / 17.86 / 18.53 / 10.94         | 84.66 / 39.29 / -4.29 / -1.72            | emaSell=true, momentumDown=true                      |
| 21:45    | 64540.00 | 0.69153 | BUY   | BUY   | FLAT  | FLAT  | 57.55 / 25.73 / 7.88 / 13.20          | 84.81 / 31.55 / -7.74 / -6.01            | emaSell=true, momentumDown=true                      |
| 21:46    | 64505.99 | 0.64750 | BUY   | BUY   | FLAT  | FLAT  | 54.83 / 23.56 / 5.70 / 12.12          | 81.25 / 22.40 / -9.16 / -8.45            | strongPositive=true, momentumDown=true               |
| 21:48    | 64507.98 | 0.65005 | BUY   | BUY   | SELL  | FLAT  | 54.99 / 23.69 / 5.83 / 12.18          | 75.36 / 9.91 / -6.54 / -6.24             | emaSell=true, momentumDown=true, strongPositive=true |
| 21:50    | 64506.24 | 0.65872 | BUY   | BUY   | FLAT  | FLAT  | 63.43 / 25.71 / 2.02 / 3.92           | 71.17 / 4.58 / -5.33 / -5.94             | emaSell=true, momentumDown=true                      |
| 21:52    | 64454.01 | 0.59844 | BUY   | BUY   | FLAT  | FLAT  | 59.27 / 22.37 / -1.32 / 2.26          | 57.75 / -6.90 / -6.05 / -5.74            | momentumDown=true                                    |
| 21:54    | 64494.08 | 0.64489 | BUY   | BUY   | FLAT  | FLAT  | 62.47 / 24.93 / 1.24 / 3.54           | 51.60 / -8.94 / -1.41 / -1.02            | momentumDown=true                                    |
==============================================
| pUp       | Meaning                                     |
| --------- | ------------------------------------------- |
| ≤ 0.20    | extremely strong BUY / very oversold region |
| 0.20–0.27 | strong BUY                                  |
| 0.27–0.32 | medium BUY                                  |
| 0.32–0.34 | weak BUY edge                               |
| > 0.34    | no BUY                                      |
