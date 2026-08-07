
chidi@localhost:~$
chidi@localhost:~$ what-state --all --n 8
=== BUY last 8 ===
{
  "OpenPrice": 64871.130000000005,
  "Side": "BUY",
  "SizeBase": 0.00031,
  "Take": 65969.71277826214,
  "OpenTime": "2026-08-07T14:08:00Z",
  "EntryFee": 0.0201100503,
  "OpenNotionalUSD": 20.1100503,
  "TrailActive": false,
  "TrailPeak": 0,
  "TrailStop": 0,
  "reason": "bottom_reversal_buy|macd_idx6=-63.799706|eps=63.149903|buffer=15.00|threshold=-48.149903|macd_zone=true|ema_low_bottom=true|pyramid_buy=true",
  "est_exit_fee_usd": 0.020110599,
  "unrealized_pnl_usd": -0.039671949300000994,
  "exit_mode": "ScalpFixedTP",
  "version": 162,
  "confidence_mult": 0.25206617151988037,
  "profit_gate_usd": 0.3,
  "entry_method": "Case11BBottomReversal",
  "activate_gate_usd": 0.3,
  "distance_pct": 0,
  "refund_portion_usd": 0,
  "entry_order_id": "65230072600",
  "case3_b_replacement_started": false,
  "case3_b_replacement_order_id": "",
  "entry_producer": "Case11BBottomReversal"
}
=== SELL last 8 ===
{
  "OpenPrice": 64840,
  "Side": "SELL",
  "SizeBase": 0.00105,
  "Take": 64142.261238999105,
  "OpenTime": "2026-08-07T09:28:00Z",
  "EntryFee": 0.06808199999999999,
  "OpenNotionalUSD": 68.082,
  "TrailActive": false,
  "TrailPeak": 0,
  "TrailStop": 0,
  "reason": "peak_sell|confidence=1.00|regime=UP|near_peak_pct=0.070770|macd_idx6=43.175027|macd_line=46.688891|macd_hist=0.615607|ema_high_peak=true|spacing=true|pending=0|adverse_required=false|sell_latched=64987.26000000|adverse_reached=false|adverse_pass=true",
  "est_exit_fee_usd": 0.068116545,
  "unrealized_pnl_usd": -0.17074354500000152,
  "exit_mode": "ScalpFixedTP",
  "version": 162,
  "confidence_mult": 1,
  "profit_gate_usd": 0.5971943247499998,
  "entry_method": "Case13APeakSell",
  "activate_gate_usd": 0.5971943247499998,
  "distance_pct": 0,
  "refund_portion_usd": 0,
  "entry_order_id": "65222655264",
  "case3_b_replacement_started": false,
  "case3_b_replacement_order_id": "",
  "entry_producer": "Case13APeakSell"
}
{
  "OpenPrice": 64546.060000000005,
  "Side": "SELL",
  "SizeBase": 0.00077,
  "Take": 63768.394895753605,
  "OpenTime": "2026-08-05T12:25:00Z",
  "EntryFee": 0.0497004662,
  "OpenNotionalUSD": 49.7004662,
  "TrailActive": false,
  "TrailPeak": 0,
  "TrailStop": 0,
  "reason": "gatePrice=64545.080|latched=64545.080|effPct=0.080|basePct=1.500|elapsedHr=31.6|latchTargetHr=0.44|targetNetUSD=0.50 | pUp=0.76219|buyTh=0.40259|sellTh=0.56186|confidence=1.00|regime=UP|regimeMult=3.00|logicEPS=38.40000|logicBaseEPS=80.00000|logicRegimeEPS=192.00000|logic_macd_line=103.15140|logic_macd_line_prev6=61.39734|logic_macd_turn=97.93200|logic_macd_hist=22.72241|logic_macd_dhist=-1.48348|logic_macd_dsmooth=-3.25634|logic_macd_strong_positive=true|logic_macd_strong_negative=false|logic_macd_momentum_down=true|logic_macd_momentum_up=false|logic_ema_spread=0.000664|logic_ema2050=0.001738|logic_pattern_high_peak=false|logic_pattern_low_bottom=false|logic_pattern_price_down_up=false|logic_pattern_price_up_down=true|logic_pattern_buy=false|logic_pattern_sell=true|macd_pre_peak_zone=true|peak_reversal_sell=false|macd_pre_bottom_zone=false|bottom_reversal_buy=false|case13B_near_low_pct=0.931962|case13B_price_near_low=false|case13A_near_high_pct=0.054876|case13A_price_near_high=true|pyr_buy_side=BUY|pyr_buy_spacingPass=true|pyr_buy_gatePass=false|pyr_buy_price=64545.99000000|pyr_buy_anchor=63950.00000000|pyr_buy_gatePrice=63950.00000000|pyr_buy_latched=63950.00000000|pyr_buy_basePct=1.5000|pyr_buy_decayedPct=0.4000|pyr_buy_gateMult=0.2000|pyr_buy_effPct=0.0800|pyr_buy_elapsedHr=144.30|pyr_buy_tFloorHr=0.22|pyr_buy_soft=false|pyr_buy_hardLatch=false|pyr_buy_usedLatch=true|pyr_buy_adversePass=false|pyr_sell_side=SELL|pyr_sell_spacingPass=true|pyr_sell_gatePass=true|pyr_sell_price=64545.99000000|pyr_sell_anchor=64055.04000000|pyr_sell_gatePrice=64545.08006500|pyr_sell_latched=64545.08006500|pyr_sell_basePct=1.5000|pyr_sell_decayedPct=0.4000|pyr_sell_gateMult=0.2000|pyr_sell_effPct=0.0800|pyr_sell_elapsedHr=31.63|pyr_sell_tFloorHr=0.22|pyr_sell_soft=false|pyr_sell_hardLatch=false|pyr_sell_usedLatch=true|pyr_sell_adversePass=true|equity_legacy=SELL|equity_equity=653.24|equity_baseline=652.94|equity_buyMult=0.980000|equity_sellMult=1.020000|equity_buyTriggerUSD=639.88|equity_sellTriggerUSD=666.00|equity_buyDistanceUSD=13.35|equity_sellDistanceUSD=-12.76|equity_buyPassed=false|equity_sellPassed=false|equity_rawSpareQuote=495.09453907|equity_rawSpareBase=0.00078871|equity_spareQuote=495.09453907|equity_spareBase=0.00078871|equity_proposedBuyQuote=495.09453906|equity_proposedSellBase=0.00078000|equity_buyTrigger=false|equity_sellTrigger=false|selected_pyramid_pass=true|selected_equity_pass=false|selected_pyramid_side=SELL|selected_pyramid_spacingPass=true|selected_pyramid_gatePass=true|selected_pyramid_price=64545.99000000|selected_pyramid_anchor=64055.04000000|selected_pyramid_gatePrice=64545.08006500|selected_pyramid_latched=64545.08006500|selected_pyramid_basePct=1.5000|selected_pyramid_decayedPct=0.4000|selected_pyramid_gateMult=0.2000|selected_pyramid_effPct=0.0800|selected_pyramid_elapsedHr=31.63|selected_pyramid_tFloorHr=0.22|selected_pyramid_soft=false|selected_pyramid_hardLatch=false|selected_pyramid_usedLatch=true|selected_equity_legacy=SELL|selected_equity_equity=653.24|selected_equity_baseline=652.94|selected_equity_buyMult=0.980000|selected_equity_sellMult=1.020000|selected_equity_buyTriggerUSD=639.88|selected_equity_sellTriggerUSD=666.00|selected_equity_buyDistanceUSD=13.35|selected_equity_sellDistanceUSD=-12.76|selected_equity_buyPassed=false|selected_equity_sellPassed=false|selected_equity_rawSpareQuote=495.09453907|selected_equity_rawSpareBase=0.00078871|selected_equity_spareQuote=495.09453907|selected_equity_spareBase=0.00078871|selected_equity_proposedBuyQuote=495.09453906|selected_equity_proposedSellBase=0.00078000|selected_equity_buyTrigger=false|selected_equity_sellTrigger=false|legacySignal=SELL|logicOpinion=SELL|Producer=NormalLegacy|aiRaw=SELL|final=SELL",
  "est_exit_fee_usd": 0.049952132999999996,
  "unrealized_pnl_usd": -0.35131939919999733,
  "exit_mode": "ScalpFixedTP",
  "version": 160,
  "confidence_mult": 1,
  "profit_gate_usd": 0.5,
  "entry_method": "NormalLegacy",
  "activate_gate_usd": 0.5,
  "distance_pct": 0,
  "refund_portion_usd": 0,
  "entry_order_id": "65167687352",
  "case3_b_replacement_started": false,
  "case3_b_replacement_order_id": "",
  "entry_producer": "NormalLegacy"
}
{
  "OpenPrice": 63910.94000000001,
  "Side": "SELL",
  "SizeBase": 0.00061,
  "Take": 63082.62911933963,
  "OpenTime": "2026-08-03T14:58:00Z",
  "EntryFee": 0.038985673400000004,
  "OpenNotionalUSD": 38.9856734,
  "TrailActive": false,
  "TrailPeak": 0,
  "TrailStop": 0,
  "reason": "gatePrice=64146.430|latched=64146.430|effPct=0.163|basePct=1.500|elapsedHr=72.8|latchTargetHr=0.90|targetNetUSD=0.43 | pUp=0.68315|buyTh=0.39502|sellTh=0.56444|confidence=0.86|regime=UP|regimeMult=3.00|logicEPS=78.04806|logicBaseEPS=80.00000|logicRegimeEPS=192.00000|logic_macd_line=80.88334|logic_macd_line_prev6=64.49257|logic_macd_turn=79.32238|logic_macd_hist=3.44572|logic_macd_dhist=-1.86096|logic_macd_dsmooth=-0.31357|logic_macd_strong_positive=true|logic_macd_strong_negative=false|logic_macd_momentum_down=true|logic_macd_momentum_up=false|logic_ema_spread=0.000514|logic_ema2050=0.002876|logic_pattern_high_peak=true|logic_pattern_low_bottom=false|logic_pattern_price_down_up=false|logic_pattern_price_up_down=false|logic_pattern_buy=false|logic_pattern_sell=true|macd_pre_peak_zone=true|peak_reversal_sell=false|macd_pre_bottom_zone=false|bottom_reversal_buy=false|case13B_near_low_pct=0.000000|case13B_price_near_low=false|case13A_near_high_pct=0.090716|case13A_price_near_high=true|pyr_buy_side=BUY|pyr_buy_spacingPass=true|pyr_buy_gatePass=false|pyr_buy_price=63910.87000000|pyr_buy_anchor=62300.00000000|pyr_buy_gatePrice=62982.38000000|pyr_buy_latched=62982.38000000|pyr_buy_basePct=1.5000|pyr_buy_decayedPct=0.4000|pyr_buy_gateMult=0.4065|pyr_buy_effPct=0.1626|pyr_buy_elapsedHr=98.85|pyr_buy_tFloorHr=0.45|pyr_buy_soft=false|pyr_buy_hardLatch=false|pyr_buy_usedLatch=true|pyr_buy_adversePass=false|pyr_sell_side=SELL|pyr_sell_spacingPass=true|pyr_sell_gatePass=false|pyr_sell_price=63910.87000000|pyr_sell_anchor=63968.90000000|pyr_sell_gatePrice=64146.43019444|pyr_sell_latched=64146.43019444|pyr_sell_basePct=1.5000|pyr_sell_decayedPct=0.4000|pyr_sell_gateMult=0.4065|pyr_sell_effPct=0.1626|pyr_sell_elapsedHr=72.77|pyr_sell_tFloorHr=0.45|pyr_sell_soft=false|pyr_sell_hardLatch=false|pyr_sell_usedLatch=true|pyr_sell_adversePass=false|equity_legacy=SELL|equity_equity=652.46|equity_baseline=650.50|equity_buyMult=0.980000|equity_sellMult=1.020000|equity_buyTriggerUSD=637.49|equity_sellTriggerUSD=663.51|equity_buyDistanceUSD=14.97|equity_sellDistanceUSD=-11.05|equity_buyPassed=false|equity_sellPassed=false|equity_rawSpareQuote=489.49950798|equity_rawSpareBase=0.00062054|equity_spareQuote=489.49950798|equity_spareBase=0.00062054|equity_proposedBuyQuote=489.49950798|equity_proposedSellBase=0.00062000|equity_buyTrigger=false|equity_sellTrigger=false|selected_pyramid_pass=true|selected_equity_pass=false|selected_pyramid_side=SELL|selected_pyramid_spacingPass=true|selected_pyramid_gatePass=false|selected_pyramid_price=63910.87000000|selected_pyramid_anchor=63968.90000000|selected_pyramid_gatePrice=64146.43019444|selected_pyramid_latched=64146.43019444|selected_pyramid_basePct=1.5000|selected_pyramid_decayedPct=0.4000|selected_pyramid_gateMult=0.4065|selected_pyramid_effPct=0.1626|selected_pyramid_elapsedHr=72.77|selected_pyramid_tFloorHr=0.45|selected_pyramid_soft=false|selected_pyramid_hardLatch=false|selected_pyramid_usedLatch=true|selected_equity_legacy=SELL|selected_equity_equity=652.46|selected_equity_baseline=650.50|selected_equity_buyMult=0.980000|selected_equity_sellMult=1.020000|selected_equity_buyTriggerUSD=637.49|selected_equity_sellTriggerUSD=663.51|selected_equity_buyDistanceUSD=14.97|selected_equity_sellDistanceUSD=-11.05|selected_equity_buyPassed=false|selected_equity_sellPassed=false|selected_equity_rawSpareQuote=489.49950798|selected_equity_rawSpareBase=0.00062054|selected_equity_spareQuote=489.49950798|selected_equity_spareBase=0.00062054|selected_equity_proposedBuyQuote=489.49950798|selected_equity_proposedSellBase=0.00062000|selected_equity_buyTrigger=false|selected_equity_sellTrigger=false|legacySignal=SELL|logicOpinion=SELL|source=Case13APeakSell|aiRaw=SELL|final=SELL",
  "est_exit_fee_usd": 0.039572469,
  "unrealized_pnl_usd": -0.665353742399995,
  "exit_mode": "ScalpFixedTP",
  "version": 156,
  "confidence_mult": 0.8556071200800817,
  "profit_gate_usd": 0.42780356004004083,
  "activate_gate_usd": 0.42780356004004083,
  "distance_pct": 0,
  "refund_portion_usd": 0,
  "entry_order_id": "65111078208",
  "case3_b_replacement_started": false,
  "case3_b_replacement_order_id": ""
}
=== EXIT last 8 ===
{
  "time": "2026-08-07T11:22:09.561638192Z",
  "side": "BUY",
  "open_price": 64209.299999999996,
  "close_price": 65040.66,
  "size_base": 0.00086,
  "open_notional_usd": 55.219998,
  "entry_fee_usd": 0.05521999799999999,
  "exit_fee_usd": 0.0559349676,
  "pnl_usd": 0.6038146344000067,
  "reason": "take_profit | exitReason{side=BUY|regime=UP|regimeMult=3.00|exitReason=take_profit|exitClass=L1_PROFIT_GATE|exitNetPNL=0.59822}  ||  openReason{bottom_buy|confidence=0.69|regime=DOWN|price=64209.37000000|recent_low=64172.00000000|near_low_pct=0.058234|macd_idx6=-23.370877|macd_line=-24.314232|macd_hist=-0.235691|ema_low_bottom=true|spacing=true|pending=0|adverse_required=false|buy_latched=63991.47983333|adverse_reached=false|adverse_pass=true}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "65212907642",
  "exit_order_id": "65224308890",
  "version": 162
}
{
  "time": "2026-08-07T09:24:51.091394235Z",
  "side": "BUY",
  "open_price": 64217.909999999996,
  "close_price": 64815.490000000005,
  "size_base": 0.00129,
  "open_notional_usd": 82.8411039,
  "entry_fee_usd": 0.08284110389999998,
  "exit_fee_usd": 0.08361198210000001,
  "pnl_usd": 0.6044251140000116,
  "reason": "take_profit | exitReason{side=BUY|regime=UP|regimeMult=3.00|exitReason=take_profit|exitClass=L1_PROFIT_GATE|exitNetPNL=0.59606}  ||  openReason{bottom_buy|confidence=0.69|regime=DOWN|price=64217.98000000|recent_low=64172.00000000|near_low_pct=0.071651|macd_idx6=-23.370877|macd_line=-24.314232|macd_hist=-0.235691|ema_low_bottom=true|spacing=true|pending=0|adverse_required=false|buy_latched=64030.91669444|adverse_reached=false|adverse_pass=true}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "65212916667",
  "exit_order_id": "65222555374",
  "version": 162
}
{
  "time": "2026-08-05T19:07:14.689320157Z",
  "side": "SELL",
  "open_price": 64055.04,
  "close_price": 64872.02000000001,
  "size_base": 0.00105,
  "open_notional_usd": 67.257792,
  "entry_fee_usd": 0.06725779199999998,
  "exit_fee_usd": 0.068115621,
  "pnl_usd": -0.993202413000011,
  "reason": "threshold_stop_loss | exitReason{side=SELL|regime=UP|regimeMult=3.00|exitReason=threshold_stop_loss|exitClass=L1_THRESHOLD_WARNING|exitNetPNL=-1.00002|stopLossPNL=1.00000|stopLossLimit=-1.00000}  ||  openReason{gatePrice=64088.871|latched=64088.871|effPct=0.101|basePct=1.500|elapsedHr=9.2|latchTargetHr=0.56|targetNetUSD=0.48 | pUp=0.69573|buyTh=0.40934|sellTh=0.55662|confidence=0.96|regime=UP|regimeMult=3.00|logicEPS=48.50217|logicBaseEPS=80.00000|logicRegimeEPS=192.00000|logic_macd_line=56.59901|logic_macd_line_prev6=28.00645|logic_macd_turn=49.32274|logic_macd_hist=15.77820|logic_macd_dhist=-1.04426|logic_macd_dsmooth=-0.43695|logic_macd_strong_positive=true|logic_macd_strong_negative=false|logic_macd_momentum_down=true|logic_macd_momentum_up=false|logic_ema_spread=0.000487|logic_ema2050=0.000998|logic_pattern_high_peak=true|logic_pattern_low_bottom=false|logic_pattern_price_down_up=false|logic_pattern_price_up_down=false|logic_pattern_buy=false|logic_pattern_sell=true|macd_pre_peak_zone=false|peak_reversal_sell=false|macd_pre_bottom_zone=false|bottom_reversal_buy=false|case13B_near_low_pct=0.000000|case13B_price_near_low=false|case13A_near_high_pct=0.038811|case13A_price_near_high=true|pyr_buy_side=BUY|pyr_buy_spacingPass=true|pyr_buy_gatePass=false|pyr_buy_price=64055.04000000|pyr_buy_anchor=63322.01000000|pyr_buy_gatePrice=62982.38000000|pyr_buy_latched=62982.38000000|pyr_buy_basePct=1.5000|pyr_buy_decayedPct=0.4000|pyr_buy_gateMult=0.2526|pyr_buy_effPct=0.1010|pyr_buy_elapsedHr=112.64|pyr_buy_tFloorHr=0.28|pyr_buy_soft=false|pyr_buy_hardLatch=false|pyr_buy_usedLatch=true|pyr_buy_adversePass=false|pyr_sell_side=SELL|pyr_sell_spacingPass=true|pyr_sell_gatePass=false|pyr_sell_price=64055.04000000|pyr_sell_anchor=63910.94000000|pyr_sell_gatePrice=64088.87066667|pyr_sell_latched=64088.87066667|pyr_sell_basePct=1.5000|pyr_sell_decayedPct=0.4000|pyr_sell_gateMult=0.2526|pyr_sell_effPct=0.1010|pyr_sell_elapsedHr=9.19|pyr_sell_tFloorHr=0.28|pyr_sell_soft=false|pyr_sell_hardLatch=false|pyr_sell_usedLatch=true|pyr_sell_adversePass=false|equity_legacy=SELL|equity_equity=653.01|equity_baseline=652.34|equity_buyMult=0.980000|equity_sellMult=1.020000|equity_buyTriggerUSD=639.30|equity_sellTriggerUSD=665.39|equity_buyDistanceUSD=13.71|equity_sellDistanceUSD=-12.38|equity_buyPassed=false|equity_sellPassed=false|equity_rawSpareQuote=545.39082023|equity_rawSpareBase=0.00106948|equity_spareQuote=545.39082023|equity_spareBase=0.00106948|equity_proposedBuyQuote=545.39082022|equity_proposedSellBase=0.00106000|equity_buyTrigger=false|equity_sellTrigger=false|selected_pyramid_pass=true|selected_equity_pass=false|selected_pyramid_side=SELL|selected_pyramid_spacingPass=true|selected_pyramid_gatePass=false|selected_pyramid_price=64055.04000000|selected_pyramid_anchor=63910.94000000|selected_pyramid_gatePrice=64088.87066667|selected_pyramid_latched=64088.87066667|selected_pyramid_basePct=1.5000|selected_pyramid_decayedPct=0.4000|selected_pyramid_gateMult=0.2526|selected_pyramid_effPct=0.1010|selected_pyramid_elapsedHr=9.19|selected_pyramid_tFloorHr=0.28|selected_pyramid_soft=false|selected_pyramid_hardLatch=false|selected_pyramid_usedLatch=true|selected_equity_legacy=SELL|selected_equity_equity=653.01|selected_equity_baseline=652.34|selected_equity_buyMult=0.980000|selected_equity_sellMult=1.020000|selected_equity_buyTriggerUSD=639.30|selected_equity_sellTriggerUSD=665.39|selected_equity_buyDistanceUSD=13.71|selected_equity_sellDistanceUSD=-12.38|selected_equity_buyPassed=false|selected_equity_sellPassed=false|selected_equity_rawSpareQuote=545.39082023|selected_equity_rawSpareBase=0.00106948|selected_equity_spareQuote=545.39082023|selected_equity_spareBase=0.00106948|selected_equity_proposedBuyQuote=545.39082022|selected_equity_proposedSellBase=0.00106000|selected_equity_buyTrigger=false|selected_equity_sellTrigger=false|legacySignal=SELL|logicOpinion=SELL|source=Case13APeakSell|aiRaw=SELL|final=SELL}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "65129474272",
  "exit_order_id": "65181267880",
  "version": 160
}
{
  "time": "2026-08-05T19:04:17.566580012Z",
  "side": "BUY",
  "open_price": 63930,
  "close_price": 64850.86,
  "size_base": 0.00039,
  "open_notional_usd": 24.9327,
  "entry_fee_usd": 0.0249327,
  "exit_fee_usd": 0.0252918354,
  "pnl_usd": 0.3089108646000002,
  "reason": "take_profit | exitReason{side=BUY|regime=UP|regimeMult=3.00|exitReason=take_profit|exitClass=L1_PROFIT_GATE|exitNetPNL=0.30638}  ||  openReason{gatePrice=63950.000|latched=63950.000|effPct=0.383|basePct=1.500|elapsedHr=145.5|latchTargetHr=2.11|targetNetUSD=0.30 | pUp=0.34699|buyTh=0.40259|sellTh=0.56186|confidence=0.32|regime=UP|regimeMult=3.00|logicEPS=61.20854|logicBaseEPS=80.00000|logicRegimeEPS=64.00000|logic_macd_line=-80.52442|logic_macd_line_prev6=-35.98262|logic_macd_turn=-71.57194|logic_macd_hist=-19.28955|logic_macd_dhist=1.92233|logic_macd_dsmooth=0.58644|logic_macd_strong_positive=false|logic_macd_strong_negative=true|logic_macd_momentum_down=false|logic_macd_momentum_up=true|logic_ema_spread=-0.000693|logic_ema2050=-0.001605|logic_pattern_high_peak=false|logic_pattern_low_bottom=true|logic_pattern_price_down_up=false|logic_pattern_price_up_down=false|logic_pattern_buy=true|logic_pattern_sell=false|macd_pre_peak_zone=false|peak_reversal_sell=false|macd_pre_bottom_zone=false|bottom_reversal_buy=false|case13B_near_low_pct=0.060901|case13B_price_near_low=true|case13A_near_high_pct=1.008696|case13A_price_near_high=false|pyr_buy_side=BUY|pyr_buy_spacingPass=true|pyr_buy_gatePass=true|pyr_buy_price=63930.00000000|pyr_buy_anchor=63891.09000000|pyr_buy_gatePrice=63950.00000000|pyr_buy_latched=63950.00000000|pyr_buy_basePct=1.5000|pyr_buy_decayedPct=0.4000|pyr_buy_gateMult=0.9564|pyr_buy_effPct=0.3826|pyr_buy_elapsedHr=145.48|pyr_buy_tFloorHr=1.05|pyr_buy_soft=false|pyr_buy_hardLatch=false|pyr_buy_usedLatch=true|pyr_buy_adversePass=true|pyr_sell_side=SELL|pyr_sell_spacingPass=true|pyr_sell_gatePass=false|pyr_sell_price=63930.00000000|pyr_sell_anchor=64546.06000000|pyr_sell_gatePrice=64581.43000000|pyr_sell_latched=0.00000000|pyr_sell_basePct=1.5000|pyr_sell_decayedPct=0.4000|pyr_sell_gateMult=0.9564|pyr_sell_effPct=0.3826|pyr_sell_elapsedHr=1.18|pyr_sell_tFloorHr=1.05|pyr_sell_soft=true|pyr_sell_hardLatch=false|pyr_sell_usedLatch=false|pyr_sell_adversePass=false|equity_legacy=BUY|equity_equity=653.19|equity_baseline=653.21|equity_buyMult=0.980000|equity_sellMult=1.020000|equity_buyTriggerUSD=640.14|equity_sellTriggerUSD=666.27|equity_buyDistanceUSD=13.05|equity_sellDistanceUSD=-13.08|equity_buyPassed=false|equity_sellPassed=false|equity_rawSpareQuote=496.49354464|equity_rawSpareBase=0.00001871|equity_spareQuote=496.49354464|equity_spareBase=0.00001871|equity_proposedBuyQuote=496.49354464|equity_proposedSellBase=0.00001000|equity_buyTrigger=false|equity_sellTrigger=false|selected_pyramid_pass=true|selected_equity_pass=false|selected_pyramid_side=BUY|selected_pyramid_spacingPass=true|selected_pyramid_gatePass=true|selected_pyramid_price=63930.00000000|selected_pyramid_anchor=63891.09000000|selected_pyramid_gatePrice=63950.00000000|selected_pyramid_latched=63950.00000000|selected_pyramid_basePct=1.5000|selected_pyramid_decayedPct=0.4000|selected_pyramid_gateMult=0.9564|selected_pyramid_effPct=0.3826|selected_pyramid_elapsedHr=145.48|selected_pyramid_tFloorHr=1.05|selected_pyramid_soft=false|selected_pyramid_hardLatch=false|selected_pyramid_usedLatch=true|selected_equity_legacy=BUY|selected_equity_equity=653.19|selected_equity_baseline=653.21|selected_equity_buyMult=0.980000|selected_equity_sellMult=1.020000|selected_equity_buyTriggerUSD=640.14|selected_equity_sellTriggerUSD=666.27|selected_equity_buyDistanceUSD=13.05|selected_equity_sellDistanceUSD=-13.08|selected_equity_buyPassed=false|selected_equity_sellPassed=false|selected_equity_rawSpareQuote=496.49354464|selected_equity_rawSpareBase=0.00001871|selected_equity_spareQuote=496.49354464|selected_equity_spareBase=0.00001871|selected_equity_proposedBuyQuote=496.49354464|selected_equity_proposedSellBase=0.00001000|selected_equity_buyTrigger=false|selected_equity_sellTrigger=false|legacySignal=BUY|logicOpinion=BUY|Producer=NormalLegacy|aiRaw=BUY|final=BUY}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "65169643099",
  "exit_order_id": "65180967323",
  "version": 160
}
{
  "time": "2026-08-04T00:35:20.895359795Z",
  "side": "SELL",
  "open_price": 63910.939999999995,
  "close_price": 63365.560000000005,
  "size_base": 0.00106,
  "open_notional_usd": 67.7456573,
  "entry_fee_usd": 0.0677456573,
  "exit_fee_usd": 0.0671674936,
  "pnl_usd": 0.4431896490999894,
  "reason": "take_profit | exitReason{side=SELL|regime=UP|regimeMult=3.00|exitReason=take_profit|exitClass=L1_PROFIT_GATE|exitNetPNL=0.43646}  ||  openReason{gatePrice=64146.430|latched=64146.430|effPct=0.163|basePct=1.500|elapsedHr=72.8|latchTargetHr=0.90|targetNetUSD=0.43 | pUp=0.68315|buyTh=0.39502|sellTh=0.56444|confidence=0.86|regime=UP|regimeMult=3.00|logicEPS=78.04806|logicBaseEPS=80.00000|logicRegimeEPS=192.00000|logic_macd_line=80.88334|logic_macd_line_prev6=64.49257|logic_macd_turn=79.32238|logic_macd_hist=3.44572|logic_macd_dhist=-1.86096|logic_macd_dsmooth=-0.31357|logic_macd_strong_positive=true|logic_macd_strong_negative=false|logic_macd_momentum_down=true|logic_macd_momentum_up=false|logic_ema_spread=0.000514|logic_ema2050=0.002876|logic_pattern_high_peak=true|logic_pattern_low_bottom=false|logic_pattern_price_down_up=false|logic_pattern_price_up_down=false|logic_pattern_buy=false|logic_pattern_sell=true|macd_pre_peak_zone=true|peak_reversal_sell=false|macd_pre_bottom_zone=false|bottom_reversal_buy=false|case13B_near_low_pct=0.000000|case13B_price_near_low=false|case13A_near_high_pct=0.090716|case13A_price_near_high=true|pyr_buy_side=BUY|pyr_buy_spacingPass=true|pyr_buy_gatePass=false|pyr_buy_price=63910.87000000|pyr_buy_anchor=62300.00000000|pyr_buy_gatePrice=62982.38000000|pyr_buy_latched=62982.38000000|pyr_buy_basePct=1.5000|pyr_buy_decayedPct=0.4000|pyr_buy_gateMult=0.4065|pyr_buy_effPct=0.1626|pyr_buy_elapsedHr=98.85|pyr_buy_tFloorHr=0.45|pyr_buy_soft=false|pyr_buy_hardLatch=false|pyr_buy_usedLatch=true|pyr_buy_adversePass=false|pyr_sell_side=SELL|pyr_sell_spacingPass=true|pyr_sell_gatePass=false|pyr_sell_price=63910.87000000|pyr_sell_anchor=63968.90000000|pyr_sell_gatePrice=64146.43019444|pyr_sell_latched=64146.43019444|pyr_sell_basePct=1.5000|pyr_sell_decayedPct=0.4000|pyr_sell_gateMult=0.4065|pyr_sell_effPct=0.1626|pyr_sell_elapsedHr=72.77|pyr_sell_tFloorHr=0.45|pyr_sell_soft=false|pyr_sell_hardLatch=false|pyr_sell_usedLatch=true|pyr_sell_adversePass=false|equity_legacy=SELL|equity_equity=652.46|equity_baseline=650.50|equity_buyMult=0.980000|equity_sellMult=1.020000|equity_buyTriggerUSD=637.49|equity_sellTriggerUSD=663.51|equity_buyDistanceUSD=14.97|equity_sellDistanceUSD=-11.05|equity_buyPassed=false|equity_sellPassed=false|equity_rawSpareQuote=489.49950798|equity_rawSpareBase=0.00255054|equity_spareQuote=489.49950798|equity_spareBase=0.00255054|equity_proposedBuyQuote=489.49950798|equity_proposedSellBase=0.00255000|equity_buyTrigger=false|equity_sellTrigger=false|selected_pyramid_pass=true|selected_equity_pass=false|selected_pyramid_side=SELL|selected_pyramid_spacingPass=true|selected_pyramid_gatePass=false|selected_pyramid_price=63910.87000000|selected_pyramid_anchor=63968.90000000|selected_pyramid_gatePrice=64146.43019444|selected_pyramid_latched=64146.43019444|selected_pyramid_basePct=1.5000|selected_pyramid_decayedPct=0.4000|selected_pyramid_gateMult=0.4065|selected_pyramid_effPct=0.1626|selected_pyramid_elapsedHr=72.77|selected_pyramid_tFloorHr=0.45|selected_pyramid_soft=false|selected_pyramid_hardLatch=false|selected_pyramid_usedLatch=true|selected_equity_legacy=SELL|selected_equity_equity=652.46|selected_equity_baseline=650.50|selected_equity_buyMult=0.980000|selected_equity_sellMult=1.020000|selected_equity_buyTriggerUSD=637.49|selected_equity_sellTriggerUSD=663.51|selected_equity_buyDistanceUSD=14.97|selected_equity_sellDistanceUSD=-11.05|selected_equity_buyPassed=false|selected_equity_sellPassed=false|selected_equity_rawSpareQuote=489.49950798|selected_equity_rawSpareBase=0.00255054|selected_equity_spareQuote=489.49950798|selected_equity_spareBase=0.00255054|selected_equity_proposedBuyQuote=489.49950798|selected_equity_proposedSellBase=0.00255000|selected_equity_buyTrigger=false|selected_equity_sellTrigger=false|legacySignal=SELL|logicOpinion=SELL|source=Case13APeakSell|aiRaw=SELL|final=SELL|refund=buy-partial}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 55.60245690000001,
  "entry_order_id": "65111077309",
  "exit_order_id": "65124671188",
  "version": 156
}
{
  "time": "2026-07-31T14:12:45.832142965Z",
  "side": "SELL",
  "open_price": 63742.33,
  "close_price": 62825.97,
  "size_base": 0.00038,
  "open_notional_usd": 24.2220854,
  "entry_fee_usd": 0.024222085400000003,
  "exit_fee_usd": 0.0238738686,
  "pnl_usd": 0.3001208460000002,
  "reason": "profit_protection | exitReason{side=SELL|regime=DOWN|regimeMult=3.00|exitReason=profit_protection|exitClass=L2_PROFIT_PROTECTION|exitNetPNL=0.29773}  ||  openReason{gatePrice=63551.640|latched=63551.640|effPct=0.382|basePct=1.500|elapsedHr=22.6|latchTargetHr=2.10|targetNetUSD=0.30 | pUp=0.60281|buyTh=0.37527|sellTh=0.56537|regime=DOWN|regimeMult=3.00|confidence=0.32|logicEPS=48.90036|logic_macd_line=99.75511|logic_macd_turn=114.33234|logic_macd_hist=0.39822|logic_macd_dhist=-4.95408|logic_macd_dsmooth=-8.00742|logic_macd_strong_positive=true|logic_macd_strong_negative=false|logic_macd_momentum_down=true|logic_macd_momentum_up=false|logic_ema_spread=0.000006|logic_ema2050=0.002272|logic_pattern_high_peak=false|logic_pattern_low_bottom=false|logic_pattern_price_down_up=false|logic_pattern_price_up_down=true|logic_pattern_buy=false|logic_pattern_sell=true|aiRaw=SELL|logicOpinion=SELL|final=SELL}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "64936331653",
  "exit_order_id": "65042532531",
  "version": 147
}
{
  "time": "2026-07-31T14:06:45.552236727Z",
  "side": "SELL",
  "open_price": 63742.33,
  "close_price": 62805.47,
  "size_base": 0.00038,
  "open_notional_usd": 24.2220854,
  "entry_fee_usd": 0.024222085400000003,
  "exit_fee_usd": 0.023866078600000003,
  "pnl_usd": 0.30791863600000025,
  "reason": "take_profit | exitReason{side=SELL|regime=DOWN|regimeMult=3.00|exitReason=take_profit|exitClass=L1_PROFIT_GATE|exitNetPNL=0.30553}  ||  openReason{gatePrice=63551.640|latched=63551.640|effPct=0.382|basePct=1.500|elapsedHr=22.6|latchTargetHr=2.10|targetNetUSD=0.30 | pUp=0.60281|buyTh=0.37527|sellTh=0.56537|regime=DOWN|regimeMult=3.00|confidence=0.32|logicEPS=48.90036|logic_macd_line=99.75511|logic_macd_turn=114.33234|logic_macd_hist=0.39822|logic_macd_dhist=-4.95408|logic_macd_dsmooth=-8.00742|logic_macd_strong_positive=true|logic_macd_strong_negative=false|logic_macd_momentum_down=true|logic_macd_momentum_up=false|logic_ema_spread=0.000006|logic_ema2050=0.002272|logic_pattern_high_peak=false|logic_pattern_low_bottom=false|logic_pattern_price_down_up=false|logic_pattern_price_up_down=true|logic_pattern_buy=false|logic_pattern_sell=true|aiRaw=SELL|logicOpinion=SELL|final=SELL}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "64936331164",
  "exit_order_id": "65041956922",
  "version": 147
}
{
  "time": "2026-07-30T12:08:06.399016625Z",
  "side": "BUY",
  "open_price": 63563.299999999996,
  "close_price": 64898.59,
  "size_base": 0.00025,
  "open_notional_usd": 15.890825,
  "entry_fee_usd": 0.015890825,
  "exit_fee_usd": 0.0162246475,
  "pnl_usd": 0.3017070275000002,
  "reason": "take_profit | exitReason{side=BUY|regime=UP|regimeMult=3.00|exitReason=take_profit|exitClass=L1_PROFIT_GATE|exitNetPNL=0.30009}  ||  openReason{gatePrice=63563.370|latched=63563.370|effPct=0.400|basePct=1.500|elapsedHr=3.9|latchTargetHr=2.20|targetNetUSD=0.30 | pUp=0.39488|buyTh=0.39691|sellTh=0.55280|regime=UP|regimeMult=3.00|confidence=0.20|logicEPS=63.99826|logic_macd_line=-67.32949|logic_macd_turn=-67.32676|logic_macd_hist=-1.37728|logic_macd_dhist=1.39732|logic_macd_dsmooth=0.51762|logic_macd_strong_positive=false|logic_macd_strong_negative=true|logic_macd_momentum_down=false|logic_macd_momentum_up=true|logic_ema_spread=-0.000246|logic_ema2050=-0.001517|logic_pattern_high_peak=false|logic_pattern_low_bottom=false|logic_pattern_price_down_up=true|logic_pattern_price_up_down=false|logic_pattern_buy=true|logic_pattern_sell=false|aiRaw=BUY|logicOpinion=BUY|final=BUY}",
  "exit_mode": "ScalpFixedTP",
  "was_runner": false,
  "refund_portion_usd": 0,
  "entry_order_id": "64940635071",
  "exit_order_id": "65007490519",
  "version": 147
}
chidi@localhost:~$ what-state --all --n 8 --compact
=== BUY last 8 ===
time=2026-08-07 09:08:00 CDT side=BUY open=64871.130000000005 size=0.00031 open_notional_usd=20.1100503 unrealized_pnl_usd=-0.03967349775000018 pUp=n/a conf=0.25206617151988037 net_gate_usd=0.3 entry_id=65230072600
=== SELL last 8 ===
time=2026-08-07 04:28:00 CDT side=SELL open=64840 size=0.00105 open_notional_usd=68.082 unrealized_pnl_usd=-0.17073828975000427 pUp=n/a conf=1.00 net_gate_usd=0.5971943247499998 entry_id=65222655264
time=2026-08-05 07:25:00 CDT side=SELL open=64546.060000000005 size=0.00077 open_notional_usd=49.7004662 unrealized_pnl_usd=-0.3513155453499993 pUp=0.76219 conf=1.00 net_gate_usd=0.5 entry_id=65167687352
time=2026-08-03 09:58:00 CDT side=SELL open=63910.94000000001 size=0.00061 open_notional_usd=38.9856734 unrealized_pnl_usd=-0.6653506893499966 pUp=0.68315 conf=0.86 net_gate_usd=0.42780356004004083 entry_id=65111078208
=== EXIT last 8 ===
time=2026-08-07 06:22:09 CDT side=BUY reason=take_profit exit_class=L1_PROFIT_GATE pUp=n/a conf=0.69 d_signal=n/a pnl=0.6038146344000067 open_notional_usd=55.219998 size=0.00086 open=64209.299999999996 close=65040.66 exit_mode=ScalpFixedTP entry_id=65212907642 exit_id=65224308890
time=2026-08-07 04:24:51 CDT side=BUY reason=take_profit exit_class=L1_PROFIT_GATE pUp=n/a conf=0.69 d_signal=n/a pnl=0.6044251140000116 open_notional_usd=82.8411039 size=0.00129 open=64217.909999999996 close=64815.490000000005 exit_mode=ScalpFixedTP entry_id=65212916667 exit_id=65222555374
time=2026-08-05 14:07:14 CDT side=SELL reason=threshold_stop_loss exit_class=L1_THRESHOLD_WARNING pUp=0.69573 conf=0.96 d_signal=SELL pnl=-0.993202413000011 open_notional_usd=67.257792 size=0.00105 open=64055.04 close=64872.02000000001 exit_mode=ScalpFixedTP entry_id=65129474272 exit_id=65181267880
time=2026-08-05 14:04:17 CDT side=BUY reason=take_profit exit_class=L1_PROFIT_GATE pUp=0.34699 conf=0.32 d_signal=BUY pnl=0.3089108646000002 open_notional_usd=24.9327 size=0.00039 open=63930 close=64850.86 exit_mode=ScalpFixedTP entry_id=65169643099 exit_id=65180967323
time=2026-08-03 19:35:20 CDT side=SELL reason=take_profit exit_class=L1_PROFIT_GATE pUp=0.68315 conf=0.86 d_signal=SELL pnl=0.4431896490999894 open_notional_usd=67.7456573 size=0.00106 open=63910.939999999995 close=63365.560000000005 exit_mode=ScalpFixedTP entry_id=65111077309 exit_id=65124671188
time=2026-07-31 09:12:45 CDT side=SELL reason=profit_protection exit_class=L2_PROFIT_PROTECTION pUp=0.60281 conf=0.32 d_signal=SELL pnl=0.3001208460000002 open_notional_usd=24.2220854 size=0.00038 open=63742.33 close=62825.97 exit_mode=ScalpFixedTP entry_id=64936331653 exit_id=65042532531
time=2026-07-31 09:06:45 CDT side=SELL reason=take_profit exit_class=L1_PROFIT_GATE pUp=0.60281 conf=0.32 d_signal=SELL pnl=0.30791863600000025 open_notional_usd=24.2220854 size=0.00038 open=63742.33 close=62805.47 exit_mode=ScalpFixedTP entry_id=64936331164 exit_id=65041956922
time=2026-07-30 07:08:06 CDT side=BUY reason=take_profit exit_class=L1_PROFIT_GATE pUp=0.39488 conf=0.20 d_signal=BUY pnl=0.3017070275000002 open_notional_usd=15.890825 size=0.00025 open=63563.299999999996 close=64898.59 exit_mode=ScalpFixedTP entry_id=64940635071 exit_id=65007490519
