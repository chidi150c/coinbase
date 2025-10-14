
--
bot-1  | 2025/10/14 03:03:09 [TRACE] equity: computed=1289.23027160 (ready=true)
bot-1  | 2025/10/14 03:03:10 TRACE price_fetch px=113436.28000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:03:10 [TICK] px=113436.28 lastClose(before-step)=113436.28
bot-1  | 2025/10/14 03:03:10 TRACE TARGET [TICK] px=113436.28 lastClose(before-step)=113436.28
bot-1  | 2025/10/14 03:03:10 TRACE step.start ts=2025-10-14T03:02:12Z price=113436.28000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:03:10 TRACE trail.activate side=SELL activate_at=113439.22011000 price=113436.28000000 trough=113436.28000000 stop=113890.02512000
bot-1  | 2025/10/14 03:03:10 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:03:10 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.56759, ema4=113577.27 vs ema8=113658.59, ema4_3rd=113764.01 vs ema8_3rd=113791.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:03:10 [DEBUG] EQUITY Trading: equityUSD=1289.23 lastAddEquitySell=1307.22 pct_diff_sell=0.986237 lastAddEquityBuy=1231.33 pct_diff_buy=1.047021
bot-1  | 2025/10/14 03:03:10 FLAT [pUp=0.56759, ema4=113577.27 vs ema8=113658.59, ema4_3rd=113764.01 vs ema8_3rd=113791.01]
bot-1  | 2025/10/14 03:03:10 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 03:03:13 [SYNC] latest=2025-10-14 03:03:13.837637092 +0000 UTC history_last=2025-10-14 03:03:13.837637092 +0000 UTC len=6000
bot-1  | 2025/10/14 03:03:13 TRACE price_fetch px=113425.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:03:13 [TICK] px=113425.00 lastClose(before-step)=113425.00
bot-1  | 2025/10/14 03:03:13 TRACE TARGET [TICK] px=113425.00 lastClose(before-step)=113425.00
bot-1  | 2025/10/14 03:03:13 TRACE step.start ts=2025-10-14T03:03:13Z price=113425.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:03:13 TRACE trail.raise lotSide=SELL trough=113425.00000000 stop=113878.70000000
bot-1  | 2025/10/14 03:03:13 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:03:13 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:03:13 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.22895, ema4=113522.05 vs ema8=113610.78, ema4_3rd=113696.01 vs ema8_3rd=113747.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:03:14 [DEBUG] EQUITY Trading: equityUSD=1289.28 lastAddEquitySell=1307.22 pct_diff_sell=0.986271 lastAddEquityBuy=1231.33 pct_diff_buy=1.047058
bot-1  | 2025/10/14 03:03:14 FLAT [pUp=0.22895, ema4=113522.05 vs ema8=113610.78, ema4_3rd=113696.01 vs ema8_3rd=113747.24]
--
bot-1  | 2025/10/14 03:03:23 [TRACE] equity: computed=1289.14097902 (ready=true)
bot-1  | 2025/10/14 03:03:24 TRACE price_fetch px=113411.35000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:03:24 [TICK] px=113411.35 lastClose(before-step)=113411.35
bot-1  | 2025/10/14 03:03:24 TRACE TARGET [TICK] px=113411.35 lastClose(before-step)=113411.35
bot-1  | 2025/10/14 03:03:24 TRACE step.start ts=2025-10-14T03:03:13Z price=113411.35000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:03:24 TRACE trail.raise lotSide=SELL trough=113411.35000000 stop=113864.99540000
bot-1  | 2025/10/14 03:03:24 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:03:24 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:03:24 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.22330, ema4=113516.59 vs ema8=113607.74, ema4_3rd=113696.01 vs ema8_3rd=113747.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:03:24 [DEBUG] EQUITY Trading: equityUSD=1289.14 lastAddEquitySell=1307.22 pct_diff_sell=0.986169 lastAddEquityBuy=1231.33 pct_diff_buy=1.046949
bot-1  | 2025/10/14 03:03:24 FLAT [pUp=0.22330, ema4=113516.59 vs ema8=113607.74, ema4_3rd=113696.01 vs ema8_3rd=113747.24]
--
bot-1  | 2025/10/14 03:03:50 [TRACE] equity: computed=1289.06856629 (ready=true)
bot-1  | 2025/10/14 03:03:51 TRACE price_fetch px=113407.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:03:51 [TICK] px=113407.00 lastClose(before-step)=113407.00
bot-1  | 2025/10/14 03:03:51 TRACE TARGET [TICK] px=113407.00 lastClose(before-step)=113407.00
bot-1  | 2025/10/14 03:03:51 TRACE step.start ts=2025-10-14T03:03:13Z price=113407.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:03:51 TRACE trail.raise lotSide=SELL trough=113407.00000000 stop=113860.62800000
bot-1  | 2025/10/14 03:03:51 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:03:51 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:03:51 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.22152, ema4=113514.85 vs ema8=113606.78, ema4_3rd=113696.01 vs ema8_3rd=113747.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:03:51 [DEBUG] EQUITY Trading: equityUSD=1289.07 lastAddEquitySell=1307.22 pct_diff_sell=0.986113 lastAddEquityBuy=1231.33 pct_diff_buy=1.046890
bot-1  | 2025/10/14 03:03:51 FLAT [pUp=0.22152, ema4=113514.85 vs ema8=113606.78, ema4_3rd=113696.01 vs ema8_3rd=113747.24]
--
bot-1  | 2025/10/14 03:03:52 [TRACE] equity: computed=1289.03714105 (ready=true)
bot-1  | 2025/10/14 03:03:53 TRACE price_fetch px=113376.93000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:03:53 [TICK] px=113376.93 lastClose(before-step)=113376.93
bot-1  | 2025/10/14 03:03:53 TRACE TARGET [TICK] px=113376.93 lastClose(before-step)=113376.93
bot-1  | 2025/10/14 03:03:53 TRACE step.start ts=2025-10-14T03:03:13Z price=113376.93000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:03:53 TRACE trail.raise lotSide=SELL trough=113376.93000000 stop=113830.43772000
bot-1  | 2025/10/14 03:03:53 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:03:53 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:03:53 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.20943, ema4=113502.83 vs ema8=113600.10, ema4_3rd=113696.01 vs ema8_3rd=113747.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:03:53 [DEBUG] EQUITY Trading: equityUSD=1289.04 lastAddEquitySell=1307.22 pct_diff_sell=0.986089 lastAddEquityBuy=1231.33 pct_diff_buy=1.046865
bot-1  | 2025/10/14 03:03:53 FLAT [pUp=0.20943, ema4=113502.83 vs ema8=113600.10, ema4_3rd=113696.01 vs ema8_3rd=113747.24]
--
bot-1  | 2025/10/14 03:03:53 [TRACE] equity: computed=1288.90214720 (ready=true)
bot-1  | 2025/10/14 03:03:54 TRACE price_fetch px=113376.02000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:03:54 [TICK] px=113376.02 lastClose(before-step)=113376.02
bot-1  | 2025/10/14 03:03:54 TRACE TARGET [TICK] px=113376.02 lastClose(before-step)=113376.02
bot-1  | 2025/10/14 03:03:54 TRACE step.start ts=2025-10-14T03:03:13Z price=113376.02000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:03:54 TRACE trail.raise lotSide=SELL trough=113376.02000000 stop=113829.52408000
bot-1  | 2025/10/14 03:03:54 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:03:54 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:03:54 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.20907, ema4=113502.46 vs ema8=113599.89, ema4_3rd=113696.01 vs ema8_3rd=113747.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:03:54 [DEBUG] EQUITY Trading: equityUSD=1288.90 lastAddEquitySell=1307.22 pct_diff_sell=0.985986 lastAddEquityBuy=1231.33 pct_diff_buy=1.046755
bot-1  | 2025/10/14 03:03:54 FLAT [pUp=0.20907, ema4=113502.46 vs ema8=113599.89, ema4_3rd=113696.01 vs ema8_3rd=113747.24]
--
bot-1  | 2025/10/14 03:04:47 [TRACE] equity: computed=1288.90389803 (ready=true)
bot-1  | 2025/10/14 03:04:48 TRACE price_fetch px=113361.38000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:04:48 [TICK] px=113361.38 lastClose(before-step)=113361.38
bot-1  | 2025/10/14 03:04:48 TRACE TARGET [TICK] px=113361.38 lastClose(before-step)=113361.38
bot-1  | 2025/10/14 03:04:48 TRACE step.start ts=2025-10-14T03:04:15Z price=113361.38000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:04:48 TRACE trail.raise lotSide=SELL trough=113361.38000000 stop=113814.82552000
bot-1  | 2025/10/14 03:04:48 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:04:48 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01715, ema4=113461.14 vs ema8=113557.78, ema4_3rd=113671.26 vs ema8_3rd=113722.10, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:04:48 [DEBUG] EQUITY Trading: equityUSD=1288.90 lastAddEquitySell=1307.22 pct_diff_sell=0.985987 lastAddEquityBuy=1231.33 pct_diff_buy=1.046756
bot-1  | 2025/10/14 03:04:48 FLAT [pUp=0.01715, ema4=113461.14 vs ema8=113557.78, ema4_3rd=113671.26 vs ema8_3rd=113722.10]
bot-1  | 2025/10/14 03:04:48 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 03:04:53 [TRACE] equity: computed=1288.91144009 (ready=true)
bot-1  | 2025/10/14 03:04:54 TRACE price_fetch px=113357.47000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:04:54 [TICK] px=113357.47 lastClose(before-step)=113357.47
bot-1  | 2025/10/14 03:04:54 TRACE TARGET [TICK] px=113357.47 lastClose(before-step)=113357.47
bot-1  | 2025/10/14 03:04:54 TRACE step.start ts=2025-10-14T03:04:15Z price=113357.47000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:04:54 TRACE trail.raise lotSide=SELL trough=113357.47000000 stop=113810.89988000
bot-1  | 2025/10/14 03:04:54 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:04:54 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01669, ema4=113459.58 vs ema8=113556.91, ema4_3rd=113671.26 vs ema8_3rd=113722.10, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:04:54 [DEBUG] EQUITY Trading: equityUSD=1288.91 lastAddEquitySell=1307.22 pct_diff_sell=0.985993 lastAddEquityBuy=1231.33 pct_diff_buy=1.046763
bot-1  | 2025/10/14 03:04:54 FLAT [pUp=0.01669, ema4=113459.58 vs ema8=113556.91, ema4_3rd=113671.26 vs ema8_3rd=113722.10]
bot-1  | 2025/10/14 03:04:54 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 03:04:54 [TRACE] equity: computed=1288.81478503 (ready=true)
bot-1  | 2025/10/14 03:04:55 TRACE price_fetch px=113353.95000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:04:55 [TICK] px=113353.95 lastClose(before-step)=113353.95
bot-1  | 2025/10/14 03:04:55 TRACE TARGET [TICK] px=113353.95 lastClose(before-step)=113353.95
bot-1  | 2025/10/14 03:04:55 TRACE step.start ts=2025-10-14T03:04:15Z price=113353.95000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:04:55 TRACE trail.raise lotSide=SELL trough=113353.95000000 stop=113807.36580000
bot-1  | 2025/10/14 03:04:55 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:04:55 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01630, ema4=113458.17 vs ema8=113556.13, ema4_3rd=113671.26 vs ema8_3rd=113722.10, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:04:56 [DEBUG] EQUITY Trading: equityUSD=1288.81 lastAddEquitySell=1307.22 pct_diff_sell=0.985919 lastAddEquityBuy=1231.33 pct_diff_buy=1.046684
bot-1  | 2025/10/14 03:04:56 FLAT [pUp=0.01630, ema4=113458.17 vs ema8=113556.13, ema4_3rd=113671.26 vs ema8_3rd=113722.10]
bot-1  | 2025/10/14 03:04:56 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 03:09:17 [TRACE] equity: computed=1288.82614301 (ready=true)
bot-1  | 2025/10/14 03:09:18 TRACE price_fetch px=113350.01000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:09:18 [TICK] px=113350.01 lastClose(before-step)=113350.01
bot-1  | 2025/10/14 03:09:18 TRACE TARGET [TICK] px=113350.01 lastClose(before-step)=113350.01
bot-1  | 2025/10/14 03:09:18 TRACE step.start ts=2025-10-14T03:08:20Z price=113350.01000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:09:18 TRACE trail.raise lotSide=SELL trough=113350.01000000 stop=113803.41004000
bot-1  | 2025/10/14 03:09:18 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:09:18 [DEBUG] MA Signalled PriceDownGoingUp: BUY: HighPeak: false, PriceDownGoingUp: true, LowBottom: false, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:09:18 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.39419, ema4=113400.96 vs ema8=113456.66, ema4_3rd=113442.68 vs ema8_3rd=113527.29, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:09:18 [DEBUG] EQUITY Trading: equityUSD=1288.83 lastAddEquitySell=1307.22 pct_diff_sell=0.985928 lastAddEquityBuy=1231.33 pct_diff_buy=1.046693
bot-1  | 2025/10/14 03:09:18 FLAT [pUp=0.39419, ema4=113400.96 vs ema8=113456.66, ema4_3rd=113442.68 vs ema8_3rd=113527.29]
--
bot-1  | 2025/10/14 03:09:21 [SYNC] latest=2025-10-14 03:09:21.224945782 +0000 UTC history_last=2025-10-14 03:09:21.224945782 +0000 UTC len=6000
bot-1  | 2025/10/14 03:09:21 TRACE price_fetch px=113344.99000000 stale=false err=<nil>
bot-1  | 2025/10/14 03:09:21 [TICK] px=113344.99 lastClose(before-step)=113344.99
bot-1  | 2025/10/14 03:09:21 TRACE TARGET [TICK] px=113344.99 lastClose(before-step)=113344.99
bot-1  | 2025/10/14 03:09:21 TRACE step.start ts=2025-10-14T03:09:21Z price=113344.99000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 03:09:21 TRACE trail.raise lotSide=SELL trough=113344.99000000 stop=113798.36996000
bot-1  | 2025/10/14 03:09:21 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 03:09:21 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: true, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 03:09:21 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.10892, ema4=113378.57 vs ema8=113431.85, ema4_3rd=113418.20 vs ema8_3rd=113494.89, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 03:09:21 [DEBUG] EQUITY Trading: equityUSD=1288.78 lastAddEquitySell=1307.22 pct_diff_sell=0.985893 lastAddEquityBuy=1231.33 pct_diff_buy=1.046657
bot-1  | 2025/10/14 03:09:21 FLAT [pUp=0.10892, ema4=113378.57 vs ema8=113431.85, ema4_3rd=113418.20 vs ema8_3rd=113494.89]
--
bot-1  | 2025/10/14 04:10:32 [TRACE] equity: computed=1288.78124981 (ready=true)
bot-1  | 2025/10/14 04:10:33 TRACE price_fetch px=113339.64000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:10:33 [TICK] px=113339.64 lastClose(before-step)=113339.64
bot-1  | 2025/10/14 04:10:33 TRACE TARGET [TICK] px=113339.64 lastClose(before-step)=113339.64
bot-1  | 2025/10/14 04:10:33 TRACE step.start ts=2025-10-14T04:10:05Z price=113339.64000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:10:33 TRACE trail.raise lotSide=SELL trough=113339.64000000 stop=113792.99856000
bot-1  | 2025/10/14 04:10:33 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:10:33 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:10:33 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01772, ema4=113395.08 vs ema8=113442.76, ema4_3rd=113466.43 vs ema8_3rd=113509.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:10:33 [DEBUG] EQUITY Trading: equityUSD=1288.78 lastAddEquitySell=1307.22 pct_diff_sell=0.985893 lastAddEquityBuy=1231.33 pct_diff_buy=1.046657
bot-1  | 2025/10/14 04:10:33 FLAT [pUp=0.01772, ema4=113395.08 vs ema8=113442.76, ema4_3rd=113466.43 vs ema8_3rd=113509.24]
--
bot-1  | 2025/10/14 04:10:34 [TRACE] equity: computed=1288.73474045 (ready=true)
bot-1  | 2025/10/14 04:10:35 TRACE price_fetch px=113332.23000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:10:35 [TICK] px=113332.23 lastClose(before-step)=113332.23
bot-1  | 2025/10/14 04:10:35 TRACE TARGET [TICK] px=113332.23 lastClose(before-step)=113332.23
bot-1  | 2025/10/14 04:10:35 TRACE step.start ts=2025-10-14T04:10:05Z price=113332.23000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:10:35 TRACE trail.raise lotSide=SELL trough=113332.23000000 stop=113785.55892000
bot-1  | 2025/10/14 04:10:35 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:10:35 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:10:35 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01668, ema4=113392.11 vs ema8=113441.11, ema4_3rd=113466.43 vs ema8_3rd=113509.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:10:35 [DEBUG] EQUITY Trading: equityUSD=1288.73 lastAddEquitySell=1307.22 pct_diff_sell=0.985858 lastAddEquityBuy=1231.33 pct_diff_buy=1.046619
bot-1  | 2025/10/14 04:10:35 FLAT [pUp=0.01668, ema4=113392.11 vs ema8=113441.11, ema4_3rd=113466.43 vs ema8_3rd=113509.24]
--
bot-1  | 2025/10/14 04:10:37 [TRACE] equity: computed=1288.72751265 (ready=true)
bot-1  | 2025/10/14 04:10:38 TRACE price_fetch px=113328.34000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:10:38 [TICK] px=113328.34 lastClose(before-step)=113328.34
bot-1  | 2025/10/14 04:10:38 TRACE TARGET [TICK] px=113328.34 lastClose(before-step)=113328.34
bot-1  | 2025/10/14 04:10:38 TRACE step.start ts=2025-10-14T04:10:05Z price=113328.34000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:10:38 TRACE trail.raise lotSide=SELL trough=113328.34000000 stop=113781.65336000
bot-1  | 2025/10/14 04:10:38 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:10:38 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:10:38 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01616, ema4=113390.56 vs ema8=113440.25, ema4_3rd=113466.43 vs ema8_3rd=113509.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:10:38 [DEBUG] EQUITY Trading: equityUSD=1288.73 lastAddEquitySell=1307.22 pct_diff_sell=0.985852 lastAddEquityBuy=1231.33 pct_diff_buy=1.046613
bot-1  | 2025/10/14 04:10:38 FLAT [pUp=0.01616, ema4=113390.56 vs ema8=113440.25, ema4_3rd=113466.43 vs ema8_3rd=113509.24]
--
bot-1  | 2025/10/14 04:10:41 [TRACE] equity: computed=1288.68401114 (ready=true)
bot-1  | 2025/10/14 04:10:42 TRACE price_fetch px=113324.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:10:42 [TICK] px=113324.00 lastClose(before-step)=113324.00
bot-1  | 2025/10/14 04:10:42 TRACE TARGET [TICK] px=113324.00 lastClose(before-step)=113324.00
bot-1  | 2025/10/14 04:10:42 TRACE step.start ts=2025-10-14T04:10:05Z price=113324.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:10:42 TRACE trail.raise lotSide=SELL trough=113324.00000000 stop=113777.29600000
bot-1  | 2025/10/14 04:10:42 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:10:42 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:10:42 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01559, ema4=113388.82 vs ema8=113439.28, ema4_3rd=113466.43 vs ema8_3rd=113509.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:10:42 [DEBUG] EQUITY Trading: equityUSD=1288.68 lastAddEquitySell=1307.22 pct_diff_sell=0.985819 lastAddEquityBuy=1231.33 pct_diff_buy=1.046578
bot-1  | 2025/10/14 04:10:42 FLAT [pUp=0.01559, ema4=113388.82 vs ema8=113439.28, ema4_3rd=113466.43 vs ema8_3rd=113509.24]
--
bot-1  | 2025/10/14 04:10:42 [TRACE] equity: computed=1288.66452749 (ready=true)
bot-1  | 2025/10/14 04:10:43 TRACE price_fetch px=113317.01000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:10:43 [TICK] px=113317.01 lastClose(before-step)=113317.01
bot-1  | 2025/10/14 04:10:43 TRACE TARGET [TICK] px=113317.01 lastClose(before-step)=113317.01
bot-1  | 2025/10/14 04:10:43 TRACE step.start ts=2025-10-14T04:10:05Z price=113317.01000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:10:43 TRACE trail.raise lotSide=SELL trough=113317.01000000 stop=113770.27804000
bot-1  | 2025/10/14 04:10:43 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:10:43 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:10:43 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01472, ema4=113386.03 vs ema8=113437.73, ema4_3rd=113466.43 vs ema8_3rd=113509.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:10:44 [DEBUG] EQUITY Trading: equityUSD=1288.66 lastAddEquitySell=1307.22 pct_diff_sell=0.985804 lastAddEquityBuy=1231.33 pct_diff_buy=1.046562
bot-1  | 2025/10/14 04:10:44 FLAT [pUp=0.01472, ema4=113386.03 vs ema8=113437.73, ema4_3rd=113466.43 vs ema8_3rd=113509.24]
--
bot-1  | 2025/10/14 04:10:44 [TRACE] equity: computed=1288.63314714 (ready=true)
bot-1  | 2025/10/14 04:10:45 TRACE price_fetch px=113317.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:10:45 [TICK] px=113317.00 lastClose(before-step)=113317.00
bot-1  | 2025/10/14 04:10:45 TRACE TARGET [TICK] px=113317.00 lastClose(before-step)=113317.00
bot-1  | 2025/10/14 04:10:45 TRACE step.start ts=2025-10-14T04:10:05Z price=113317.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:10:45 TRACE trail.raise lotSide=SELL trough=113317.00000000 stop=113770.26800000
bot-1  | 2025/10/14 04:10:45 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:10:45 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:10:45 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.01472, ema4=113386.02 vs ema8=113437.73, ema4_3rd=113466.43 vs ema8_3rd=113509.24, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:10:45 [DEBUG] EQUITY Trading: equityUSD=1288.63 lastAddEquitySell=1307.22 pct_diff_sell=0.985780 lastAddEquityBuy=1231.33 pct_diff_buy=1.046536
bot-1  | 2025/10/14 04:10:45 FLAT [pUp=0.01472, ema4=113386.02 vs ema8=113437.73, ema4_3rd=113466.43 vs ema8_3rd=113509.24]
--
bot-1  | 2025/10/14 04:32:11 [TRACE] equity: computed=1288.65747926 (ready=true)
bot-1  | 2025/10/14 04:32:12 TRACE price_fetch px=113300.19000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:32:12 [TICK] px=113300.19 lastClose(before-step)=113300.19
bot-1  | 2025/10/14 04:32:12 TRACE TARGET [TICK] px=113300.19 lastClose(before-step)=113300.19
bot-1  | 2025/10/14 04:32:12 TRACE step.start ts=2025-10-14T04:31:25Z price=113300.19000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:32:12 TRACE trail.raise lotSide=SELL trough=113300.19000000 stop=113753.39076000
bot-1  | 2025/10/14 04:32:12 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:32:12 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.37843, ema4=113413.90 vs ema8=113456.73, ema4_3rd=113515.67 vs ema8_3rd=113517.90, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:32:13 [DEBUG] EQUITY Trading: equityUSD=1288.66 lastAddEquitySell=1307.22 pct_diff_sell=0.985799 lastAddEquityBuy=1231.33 pct_diff_buy=1.046556
bot-1  | 2025/10/14 04:32:13 FLAT [pUp=0.37843, ema4=113413.90 vs ema8=113456.73, ema4_3rd=113515.67 vs ema8_3rd=113517.90]
bot-1  | 2025/10/14 04:32:13 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:32:28 [TRACE] equity: computed=1288.55763678 (ready=true)
bot-1  | 2025/10/14 04:32:29 TRACE price_fetch px=113300.01000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:32:29 [TICK] px=113300.01 lastClose(before-step)=113300.01
bot-1  | 2025/10/14 04:32:29 TRACE TARGET [TICK] px=113300.01 lastClose(before-step)=113300.01
bot-1  | 2025/10/14 04:32:29 TRACE step.start ts=2025-10-14T04:32:26Z price=113300.01000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:32:29 TRACE trail.raise lotSide=SELL trough=113300.01000000 stop=113753.21004000
bot-1  | 2025/10/14 04:32:29 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:32:29 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.53705, ema4=113368.34 vs ema8=113421.90, ema4_3rd=113504.14 vs ema8_3rd=113511.00, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:32:29 [DEBUG] EQUITY Trading: equityUSD=1288.56 lastAddEquitySell=1307.22 pct_diff_sell=0.985722 lastAddEquityBuy=1231.33 pct_diff_buy=1.046475
bot-1  | 2025/10/14 04:32:29 FLAT [pUp=0.53705, ema4=113368.34 vs ema8=113421.90, ema4_3rd=113504.14 vs ema8_3rd=113511.00]
bot-1  | 2025/10/14 04:32:29 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:38:42 [TRACE] equity: computed=1288.68248477 (ready=true)
bot-1  | 2025/10/14 04:38:43 TRACE price_fetch px=113290.11000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:38:43 [TICK] px=113290.11 lastClose(before-step)=113290.11
bot-1  | 2025/10/14 04:38:43 TRACE TARGET [TICK] px=113290.11 lastClose(before-step)=113290.11
bot-1  | 2025/10/14 04:38:43 TRACE step.start ts=2025-10-14T04:38:32Z price=113290.11000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:38:43 TRACE trail.raise lotSide=SELL trough=113290.11000000 stop=113743.27044000
bot-1  | 2025/10/14 04:38:43 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:38:43 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:38:43 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.79424, ema4=113346.21 vs ema8=113379.53, ema4_3rd=113398.73 vs ema8_3rd=113418.14, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:38:44 [DEBUG] EQUITY Trading: equityUSD=1288.68 lastAddEquitySell=1307.22 pct_diff_sell=0.985818 lastAddEquityBuy=1231.33 pct_diff_buy=1.046577
bot-1  | 2025/10/14 04:38:44 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:38:44 [TRACE] equity: computed=1288.51238443 (ready=true)
bot-1  | 2025/10/14 04:38:45 TRACE price_fetch px=113286.61000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:38:45 [TICK] px=113286.61 lastClose(before-step)=113286.61
bot-1  | 2025/10/14 04:38:45 TRACE TARGET [TICK] px=113286.61 lastClose(before-step)=113286.61
bot-1  | 2025/10/14 04:38:45 TRACE step.start ts=2025-10-14T04:38:32Z price=113286.61000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:38:45 TRACE trail.raise lotSide=SELL trough=113286.61000000 stop=113739.75644000
bot-1  | 2025/10/14 04:38:45 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:38:45 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:38:45 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.79783, ema4=113344.81 vs ema8=113378.75, ema4_3rd=113398.73 vs ema8_3rd=113418.14, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:38:45 [DEBUG] EQUITY Trading: equityUSD=1288.51 lastAddEquitySell=1307.22 pct_diff_sell=0.985688 lastAddEquityBuy=1231.33 pct_diff_buy=1.046438
bot-1  | 2025/10/14 04:38:45 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:38:47 [TRACE] equity: computed=1288.54331585 (ready=true)
bot-1  | 2025/10/14 04:38:48 TRACE price_fetch px=113245.99000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:38:48 [TICK] px=113245.99 lastClose(before-step)=113245.99
bot-1  | 2025/10/14 04:38:48 TRACE TARGET [TICK] px=113245.99 lastClose(before-step)=113245.99
bot-1  | 2025/10/14 04:38:48 TRACE step.start ts=2025-10-14T04:38:32Z price=113245.99000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:38:48 TRACE trail.raise lotSide=SELL trough=113245.99000000 stop=113698.97396000
bot-1  | 2025/10/14 04:38:48 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:38:48 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:38:48 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.83574, ema4=113328.56 vs ema8=113369.73, ema4_3rd=113398.73 vs ema8_3rd=113418.14, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:38:48 [DEBUG] EQUITY Trading: equityUSD=1288.54 lastAddEquitySell=1307.22 pct_diff_sell=0.985711 lastAddEquityBuy=1231.33 pct_diff_buy=1.046464
bot-1  | 2025/10/14 04:38:48 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:39:35 [TRACE] equity: computed=1288.33667245 (ready=true)
bot-1  | 2025/10/14 04:39:36 TRACE price_fetch px=113244.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:39:36 [TICK] px=113244.00 lastClose(before-step)=113244.00
bot-1  | 2025/10/14 04:39:36 TRACE TARGET [TICK] px=113244.00 lastClose(before-step)=113244.00
bot-1  | 2025/10/14 04:39:36 TRACE step.start ts=2025-10-14T04:39:33Z price=113244.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:39:36 TRACE trail.raise lotSide=SELL trough=113244.00000000 stop=113696.97600000
bot-1  | 2025/10/14 04:39:36 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:39:36 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.97814, ema4=113302.37 vs ema8=113347.29, ema4_3rd=113429.15 vs ema8_3rd=113430.73, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:39:36 [DEBUG] EQUITY Trading: equityUSD=1288.34 lastAddEquitySell=1307.22 pct_diff_sell=0.985553 lastAddEquityBuy=1231.33 pct_diff_buy=1.046296
bot-1  | 2025/10/14 04:39:36 FLAT [pUp=0.97814, ema4=113302.37 vs ema8=113347.29, ema4_3rd=113429.15 vs ema8_3rd=113430.73]
bot-1  | 2025/10/14 04:39:36 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:39:38 [TRACE] equity: computed=1288.35925373 (ready=true)
bot-1  | 2025/10/14 04:39:39 TRACE price_fetch px=113202.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:39:39 [TICK] px=113202.00 lastClose(before-step)=113202.00
bot-1  | 2025/10/14 04:39:39 TRACE TARGET [TICK] px=113202.00 lastClose(before-step)=113202.00
bot-1  | 2025/10/14 04:39:39 TRACE step.start ts=2025-10-14T04:39:33Z price=113202.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:39:39 TRACE trail.raise lotSide=SELL trough=113202.00000000 stop=113654.80800000
bot-1  | 2025/10/14 04:39:39 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:39:39 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.98720, ema4=113285.57 vs ema8=113337.95, ema4_3rd=113429.15 vs ema8_3rd=113430.73, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:39:39 [DEBUG] EQUITY Trading: equityUSD=1288.36 lastAddEquitySell=1307.22 pct_diff_sell=0.985571 lastAddEquityBuy=1231.33 pct_diff_buy=1.046314
bot-1  | 2025/10/14 04:39:39 FLAT [pUp=0.98720, ema4=113285.57 vs ema8=113337.95, ema4_3rd=113429.15 vs ema8_3rd=113430.73]
bot-1  | 2025/10/14 04:39:39 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:39:39 [TRACE] equity: computed=1288.11683045 (ready=true)
bot-1  | 2025/10/14 04:39:40 TRACE price_fetch px=113185.90000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:39:40 [TICK] px=113185.90 lastClose(before-step)=113185.90
bot-1  | 2025/10/14 04:39:40 TRACE TARGET [TICK] px=113185.90 lastClose(before-step)=113185.90
bot-1  | 2025/10/14 04:39:40 TRACE step.start ts=2025-10-14T04:39:33Z price=113185.90000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:39:40 TRACE trail.raise lotSide=SELL trough=113185.90000000 stop=113638.64360000
bot-1  | 2025/10/14 04:39:40 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:39:40 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.98957, ema4=113279.13 vs ema8=113334.38, ema4_3rd=113429.15 vs ema8_3rd=113430.73, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:39:40 [DEBUG] EQUITY Trading: equityUSD=1288.12 lastAddEquitySell=1307.22 pct_diff_sell=0.985385 lastAddEquityBuy=1231.33 pct_diff_buy=1.046117
bot-1  | 2025/10/14 04:39:40 FLAT [pUp=0.98957, ema4=113279.13 vs ema8=113334.38, ema4_3rd=113429.15 vs ema8_3rd=113430.73]
bot-1  | 2025/10/14 04:39:40 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:39:42 [TRACE] equity: computed=1288.17519161 (ready=true)
bot-1  | 2025/10/14 04:39:43 TRACE price_fetch px=113180.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:39:43 [TICK] px=113180.00 lastClose(before-step)=113180.00
bot-1  | 2025/10/14 04:39:43 TRACE TARGET [TICK] px=113180.00 lastClose(before-step)=113180.00
bot-1  | 2025/10/14 04:39:43 TRACE step.start ts=2025-10-14T04:39:33Z price=113180.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:39:43 TRACE trail.raise lotSide=SELL trough=113180.00000000 stop=113632.72000000
bot-1  | 2025/10/14 04:39:43 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:39:43 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.99033, ema4=113276.77 vs ema8=113333.06, ema4_3rd=113429.15 vs ema8_3rd=113430.73, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:39:43 [DEBUG] EQUITY Trading: equityUSD=1288.18 lastAddEquitySell=1307.22 pct_diff_sell=0.985430 lastAddEquityBuy=1231.33 pct_diff_buy=1.046165
bot-1  | 2025/10/14 04:39:43 FLAT [pUp=0.99033, ema4=113276.77 vs ema8=113333.06, ema4_3rd=113429.15 vs ema8_3rd=113430.73]
bot-1  | 2025/10/14 04:39:43 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:39:44 [TRACE] equity: computed=1288.01806541 (ready=true)
bot-1  | 2025/10/14 04:39:45 TRACE price_fetch px=113179.85000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:39:45 [TICK] px=113179.85 lastClose(before-step)=113179.85
bot-1  | 2025/10/14 04:39:45 TRACE TARGET [TICK] px=113179.85 lastClose(before-step)=113179.85
bot-1  | 2025/10/14 04:39:45 TRACE step.start ts=2025-10-14T04:39:33Z price=113179.85000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:39:45 TRACE trail.raise lotSide=SELL trough=113179.85000000 stop=113632.56940000
bot-1  | 2025/10/14 04:39:45 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:39:45 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.99034, ema4=113276.71 vs ema8=113333.03, ema4_3rd=113429.15 vs ema8_3rd=113430.73, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:39:45 [DEBUG] EQUITY Trading: equityUSD=1288.02 lastAddEquitySell=1307.22 pct_diff_sell=0.985310 lastAddEquityBuy=1231.33 pct_diff_buy=1.046037
bot-1  | 2025/10/14 04:39:45 FLAT [pUp=0.99034, ema4=113276.71 vs ema8=113333.03, ema4_3rd=113429.15 vs ema8_3rd=113430.73]
bot-1  | 2025/10/14 04:39:45 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:47:21 [TRACE] equity: computed=1288.07759379 (ready=true)
bot-1  | 2025/10/14 04:47:22 TRACE price_fetch px=113176.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:22 [TICK] px=113176.00 lastClose(before-step)=113176.00
bot-1  | 2025/10/14 04:47:22 TRACE TARGET [TICK] px=113176.00 lastClose(before-step)=113176.00
bot-1  | 2025/10/14 04:47:22 TRACE step.start ts=2025-10-14T04:46:40Z price=113176.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:22 TRACE trail.raise lotSide=SELL trough=113176.00000000 stop=113628.70400000
bot-1  | 2025/10/14 04:47:22 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:22 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:22 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.37845, ema4=113221.30 vs ema8=113251.02, ema4_3rd=113266.61 vs ema8_3rd=113292.02, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:22 [DEBUG] EQUITY Trading: equityUSD=1288.08 lastAddEquitySell=1307.22 pct_diff_sell=0.985355 lastAddEquityBuy=1231.33 pct_diff_buy=1.046085
bot-1  | 2025/10/14 04:47:22 FLAT [pUp=0.37845, ema4=113221.30 vs ema8=113251.02, ema4_3rd=113266.61 vs ema8_3rd=113292.02]
--
bot-1  | 2025/10/14 04:47:28 [TRACE] equity: computed=1288.00307108 (ready=true)
bot-1  | 2025/10/14 04:47:29 TRACE price_fetch px=113134.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:29 [TICK] px=113134.00 lastClose(before-step)=113134.00
bot-1  | 2025/10/14 04:47:29 TRACE TARGET [TICK] px=113134.00 lastClose(before-step)=113134.00
bot-1  | 2025/10/14 04:47:29 TRACE step.start ts=2025-10-14T04:46:40Z price=113134.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:29 TRACE trail.raise lotSide=SELL trough=113134.00000000 stop=113586.53600000
bot-1  | 2025/10/14 04:47:29 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:29 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:29 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.35111, ema4=113204.50 vs ema8=113241.69, ema4_3rd=113266.61 vs ema8_3rd=113292.02, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:29 [DEBUG] EQUITY Trading: equityUSD=1288.00 lastAddEquitySell=1307.22 pct_diff_sell=0.985298 lastAddEquityBuy=1231.33 pct_diff_buy=1.046025
bot-1  | 2025/10/14 04:47:29 FLAT [pUp=0.35111, ema4=113204.50 vs ema8=113241.69, ema4_3rd=113266.61 vs ema8_3rd=113292.02]
--
bot-1  | 2025/10/14 04:47:29 [TRACE] equity: computed=1287.81155669 (ready=true)
bot-1  | 2025/10/14 04:47:30 TRACE price_fetch px=113108.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:30 [TICK] px=113108.00 lastClose(before-step)=113108.00
bot-1  | 2025/10/14 04:47:30 TRACE TARGET [TICK] px=113108.00 lastClose(before-step)=113108.00
bot-1  | 2025/10/14 04:47:30 TRACE step.start ts=2025-10-14T04:46:40Z price=113108.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:30 TRACE trail.raise lotSide=SELL trough=113108.00000000 stop=113560.43200000
bot-1  | 2025/10/14 04:47:30 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:30 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:30 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.33445, ema4=113194.10 vs ema8=113235.91, ema4_3rd=113266.61 vs ema8_3rd=113292.02, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:31 [DEBUG] EQUITY Trading: equityUSD=1287.81 lastAddEquitySell=1307.22 pct_diff_sell=0.985152 lastAddEquityBuy=1231.33 pct_diff_buy=1.045869
bot-1  | 2025/10/14 04:47:31 FLAT [pUp=0.33445, ema4=113194.10 vs ema8=113235.91, ema4_3rd=113266.61 vs ema8_3rd=113292.02]
--
bot-1  | 2025/10/14 04:47:34 [TRACE] equity: computed=1287.69483437 (ready=true)
bot-1  | 2025/10/14 04:47:35 TRACE price_fetch px=113095.98000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:35 [TICK] px=113095.98 lastClose(before-step)=113095.98
bot-1  | 2025/10/14 04:47:35 TRACE TARGET [TICK] px=113095.98 lastClose(before-step)=113095.98
bot-1  | 2025/10/14 04:47:35 TRACE step.start ts=2025-10-14T04:46:40Z price=113095.98000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:35 TRACE trail.raise lotSide=SELL trough=113095.98000000 stop=113548.36392000
bot-1  | 2025/10/14 04:47:35 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:35 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:35 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.32683, ema4=113189.29 vs ema8=113233.24, ema4_3rd=113266.61 vs ema8_3rd=113292.02, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:35 [DEBUG] EQUITY Trading: equityUSD=1287.69 lastAddEquitySell=1307.22 pct_diff_sell=0.985062 lastAddEquityBuy=1231.33 pct_diff_buy=1.045774
bot-1  | 2025/10/14 04:47:35 FLAT [pUp=0.32683, ema4=113189.29 vs ema8=113233.24, ema4_3rd=113266.61 vs ema8_3rd=113292.02]
--
bot-1  | 2025/10/14 04:47:37 [TRACE] equity: computed=1287.69483437 (ready=true)
bot-1  | 2025/10/14 04:47:38 TRACE price_fetch px=113095.97000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:38 [TICK] px=113095.97 lastClose(before-step)=113095.97
bot-1  | 2025/10/14 04:47:38 TRACE TARGET [TICK] px=113095.97 lastClose(before-step)=113095.97
bot-1  | 2025/10/14 04:47:38 TRACE step.start ts=2025-10-14T04:46:40Z price=113095.97000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:38 TRACE trail.raise lotSide=SELL trough=113095.97000000 stop=113548.35388000
bot-1  | 2025/10/14 04:47:38 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:38 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:38 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.32683, ema4=113189.29 vs ema8=113233.23, ema4_3rd=113266.61 vs ema8_3rd=113292.02, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:38 [DEBUG] EQUITY Trading: equityUSD=1287.69 lastAddEquitySell=1307.22 pct_diff_sell=0.985062 lastAddEquityBuy=1231.33 pct_diff_buy=1.045774
bot-1  | 2025/10/14 04:47:38 FLAT [pUp=0.32683, ema4=113189.29 vs ema8=113233.23, ema4_3rd=113266.61 vs ema8_3rd=113292.02]
--
bot-1  | 2025/10/14 04:47:38 [TRACE] equity: computed=1287.64082785 (ready=true)
bot-1  | 2025/10/14 04:47:39 TRACE price_fetch px=113094.21000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:39 [TICK] px=113094.21 lastClose(before-step)=113094.21
bot-1  | 2025/10/14 04:47:39 TRACE TARGET [TICK] px=113094.21 lastClose(before-step)=113094.21
bot-1  | 2025/10/14 04:47:39 TRACE step.start ts=2025-10-14T04:46:40Z price=113094.21000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:39 TRACE trail.raise lotSide=SELL trough=113094.21000000 stop=113546.58684000
bot-1  | 2025/10/14 04:47:39 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:39 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:39 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.32571, ema4=113188.58 vs ema8=113232.84, ema4_3rd=113266.61 vs ema8_3rd=113292.02, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:39 [DEBUG] EQUITY Trading: equityUSD=1287.64 lastAddEquitySell=1307.22 pct_diff_sell=0.985021 lastAddEquityBuy=1231.33 pct_diff_buy=1.045731
bot-1  | 2025/10/14 04:47:39 FLAT [pUp=0.32571, ema4=113188.58 vs ema8=113232.84, ema4_3rd=113266.61 vs ema8_3rd=113292.02]
--
bot-1  | 2025/10/14 04:47:41 [SYNC] latest=2025-10-14 04:47:41.218464109 +0000 UTC history_last=2025-10-14 04:47:41.218464109 +0000 UTC len=6000
bot-1  | 2025/10/14 04:47:41 TRACE price_fetch px=113073.37000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:41 [TICK] px=113073.37 lastClose(before-step)=113073.37
bot-1  | 2025/10/14 04:47:41 TRACE TARGET [TICK] px=113073.37 lastClose(before-step)=113073.37
bot-1  | 2025/10/14 04:47:41 TRACE step.start ts=2025-10-14T04:47:41Z price=113073.37000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:41 TRACE trail.raise lotSide=SELL trough=113073.37000000 stop=113525.66348000
bot-1  | 2025/10/14 04:47:41 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:41 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:41 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.57343, ema4=113142.50 vs ema8=113197.40, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:41 [DEBUG] EQUITY Trading: equityUSD=1287.63 lastAddEquitySell=1307.22 pct_diff_sell=0.985015 lastAddEquityBuy=1231.33 pct_diff_buy=1.045724
bot-1  | 2025/10/14 04:47:41 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:47:41 [TRACE] equity: computed=1287.53936922 (ready=true)
bot-1  | 2025/10/14 04:47:42 TRACE price_fetch px=113055.99000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:42 [TICK] px=113055.99 lastClose(before-step)=113055.99
bot-1  | 2025/10/14 04:47:42 TRACE TARGET [TICK] px=113055.99 lastClose(before-step)=113055.99
bot-1  | 2025/10/14 04:47:42 TRACE step.start ts=2025-10-14T04:47:41Z price=113055.99000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:42 TRACE trail.raise lotSide=SELL trough=113055.99000000 stop=113508.21396000
bot-1  | 2025/10/14 04:47:42 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:42 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:42 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.57677, ema4=113135.55 vs ema8=113193.54, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:42 [DEBUG] EQUITY Trading: equityUSD=1287.54 lastAddEquitySell=1307.22 pct_diff_sell=0.984943 lastAddEquityBuy=1231.33 pct_diff_buy=1.045648
bot-1  | 2025/10/14 04:47:42 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:47:43 [TRACE] equity: computed=1287.46134484 (ready=true)
bot-1  | 2025/10/14 04:47:44 TRACE price_fetch px=113053.45000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:47:44 [TICK] px=113053.45 lastClose(before-step)=113053.45
bot-1  | 2025/10/14 04:47:44 TRACE TARGET [TICK] px=113053.45 lastClose(before-step)=113053.45
bot-1  | 2025/10/14 04:47:44 TRACE step.start ts=2025-10-14T04:47:41Z price=113053.45000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:47:44 TRACE trail.raise lotSide=SELL trough=113053.45000000 stop=113505.66380000
bot-1  | 2025/10/14 04:47:44 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:47:44 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:47:44 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.57726, ema4=113134.53 vs ema8=113192.98, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:47:44 [DEBUG] EQUITY Trading: equityUSD=1287.46 lastAddEquitySell=1307.22 pct_diff_sell=0.984884 lastAddEquityBuy=1231.33 pct_diff_buy=1.045585
bot-1  | 2025/10/14 04:47:44 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:48:03 [TRACE] equity: computed=1287.50080596 (ready=true)
bot-1  | 2025/10/14 04:48:04 TRACE price_fetch px=113049.11000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:48:04 [TICK] px=113049.11 lastClose(before-step)=113049.11
bot-1  | 2025/10/14 04:48:04 TRACE TARGET [TICK] px=113049.11 lastClose(before-step)=113049.11
bot-1  | 2025/10/14 04:48:04 TRACE step.start ts=2025-10-14T04:47:41Z price=113049.11000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:48:04 TRACE trail.raise lotSide=SELL trough=113049.11000000 stop=113501.30644000
bot-1  | 2025/10/14 04:48:04 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:48:04 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:48:04 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.57812, ema4=113132.79 vs ema8=113192.01, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:48:04 [DEBUG] EQUITY Trading: equityUSD=1287.50 lastAddEquitySell=1307.22 pct_diff_sell=0.984914 lastAddEquityBuy=1231.33 pct_diff_buy=1.045617
bot-1  | 2025/10/14 04:48:04 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:48:10 [TRACE] equity: computed=1287.50179361 (ready=true)
bot-1  | 2025/10/14 04:48:11 TRACE price_fetch px=113034.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:48:11 [TICK] px=113034.00 lastClose(before-step)=113034.00
bot-1  | 2025/10/14 04:48:11 TRACE TARGET [TICK] px=113034.00 lastClose(before-step)=113034.00
bot-1  | 2025/10/14 04:48:11 TRACE step.start ts=2025-10-14T04:47:41Z price=113034.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:48:11 TRACE trail.raise lotSide=SELL trough=113034.00000000 stop=113486.13600000
bot-1  | 2025/10/14 04:48:11 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:48:11 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:48:11 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.58113, ema4=113126.75 vs ema8=113188.66, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:48:12 [DEBUG] EQUITY Trading: equityUSD=1287.50 lastAddEquitySell=1307.22 pct_diff_sell=0.984915 lastAddEquityBuy=1231.33 pct_diff_buy=1.045618
bot-1  | 2025/10/14 04:48:12 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:48:13 [TRACE] equity: computed=1287.38457746 (ready=true)
bot-1  | 2025/10/14 04:48:14 TRACE price_fetch px=113029.78000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:48:14 [TICK] px=113029.78 lastClose(before-step)=113029.78
bot-1  | 2025/10/14 04:48:14 TRACE TARGET [TICK] px=113029.78 lastClose(before-step)=113029.78
bot-1  | 2025/10/14 04:48:14 TRACE step.start ts=2025-10-14T04:47:41Z price=113029.78000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:48:14 TRACE trail.raise lotSide=SELL trough=113029.78000000 stop=113481.89912000
bot-1  | 2025/10/14 04:48:14 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:48:14 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:48:14 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.58198, ema4=113125.06 vs ema8=113187.72, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:48:15 [DEBUG] EQUITY Trading: equityUSD=1287.38 lastAddEquitySell=1307.22 pct_diff_sell=0.984825 lastAddEquityBuy=1231.33 pct_diff_buy=1.045522
bot-1  | 2025/10/14 04:48:15 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:48:15 [TRACE] equity: computed=1287.34367976 (ready=true)
bot-1  | 2025/10/14 04:48:16 TRACE price_fetch px=113020.11000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:48:16 [TICK] px=113020.11 lastClose(before-step)=113020.11
bot-1  | 2025/10/14 04:48:16 TRACE TARGET [TICK] px=113020.11 lastClose(before-step)=113020.11
bot-1  | 2025/10/14 04:48:16 TRACE step.start ts=2025-10-14T04:47:41Z price=113020.11000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:48:16 TRACE trail.raise lotSide=SELL trough=113020.11000000 stop=113472.19044000
bot-1  | 2025/10/14 04:48:16 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:48:16 [DEBUG] MA Signalled LowBottom: BUY: HighPeak: false, PriceDownGoingUp: false, LowBottom: true, PriceUpGoingDown: false
bot-1  | 2025/10/14 04:48:16 [DEBUG] Total Lots=10, Decision=BUY Reason = pUp=0.58396, ema4=113121.19 vs ema8=113185.57, ema4_3rd=113255.16 vs ema8_3rd=113280.01, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:48:16 [DEBUG] EQUITY Trading: equityUSD=1287.34 lastAddEquitySell=1307.22 pct_diff_sell=0.984794 lastAddEquityBuy=1231.33 pct_diff_buy=1.045489
bot-1  | 2025/10/14 04:48:16 [DEBUG] GATE1 lot cap reached (8); HOLD
--
bot-1  | 2025/10/14 04:49:05 [TRACE] equity: computed=1287.38507129 (ready=true)
bot-1  | 2025/10/14 04:49:06 TRACE price_fetch px=113004.84000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:49:06 [TICK] px=113004.84 lastClose(before-step)=113004.84
bot-1  | 2025/10/14 04:49:06 TRACE TARGET [TICK] px=113004.84 lastClose(before-step)=113004.84
bot-1  | 2025/10/14 04:49:06 TRACE step.start ts=2025-10-14T04:48:41Z price=113004.84000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:49:06 TRACE trail.raise lotSide=SELL trough=113004.84000000 stop=113456.85936000
bot-1  | 2025/10/14 04:49:06 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:49:06 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.75263, ema4=113090.95 vs ema8=113157.14, ema4_3rd=113251.50 vs ema8_3rd=113272.45, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:49:06 [DEBUG] EQUITY Trading: equityUSD=1287.39 lastAddEquitySell=1307.22 pct_diff_sell=0.984825 lastAddEquityBuy=1231.33 pct_diff_buy=1.045523
bot-1  | 2025/10/14 04:49:06 FLAT [pUp=0.75263, ema4=113090.95 vs ema8=113157.14, ema4_3rd=113251.50 vs ema8_3rd=113272.45]
bot-1  | 2025/10/14 04:49:06 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:27 [TRACE] equity: computed=1287.27602571 (ready=true)
bot-1  | 2025/10/14 04:50:28 TRACE price_fetch px=113003.01000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:28 [TICK] px=113003.01 lastClose(before-step)=113003.01
bot-1  | 2025/10/14 04:50:28 TRACE TARGET [TICK] px=113003.01 lastClose(before-step)=113003.01
bot-1  | 2025/10/14 04:50:28 TRACE step.start ts=2025-10-14T04:49:42Z price=113003.01000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:28 TRACE trail.raise lotSide=SELL trough=113003.01000000 stop=113455.02204000
bot-1  | 2025/10/14 04:50:28 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:28 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.14265, ema4=113067.09 vs ema8=113131.04, ema4_3rd=113188.58 vs ema8_3rd=113232.84, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:28 [DEBUG] EQUITY Trading: equityUSD=1287.28 lastAddEquitySell=1307.22 pct_diff_sell=0.984742 lastAddEquityBuy=1231.33 pct_diff_buy=1.045434
bot-1  | 2025/10/14 04:50:28 FLAT [pUp=0.14265, ema4=113067.09 vs ema8=113131.04, ema4_3rd=113188.58 vs ema8_3rd=113232.84]
bot-1  | 2025/10/14 04:50:28 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:31 [TRACE] equity: computed=1287.25223231 (ready=true)
bot-1  | 2025/10/14 04:50:32 TRACE price_fetch px=113000.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:32 [TICK] px=113000.00 lastClose(before-step)=113000.00
bot-1  | 2025/10/14 04:50:32 TRACE TARGET [TICK] px=113000.00 lastClose(before-step)=113000.00
bot-1  | 2025/10/14 04:50:32 TRACE step.start ts=2025-10-14T04:49:42Z price=113000.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:32 TRACE trail.raise lotSide=SELL trough=113000.00000000 stop=113452.00000000
bot-1  | 2025/10/14 04:50:32 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:32 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.14105, ema4=113065.89 vs ema8=113130.37, ema4_3rd=113188.58 vs ema8_3rd=113232.84, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:32 [DEBUG] EQUITY Trading: equityUSD=1287.25 lastAddEquitySell=1307.22 pct_diff_sell=0.984724 lastAddEquityBuy=1231.33 pct_diff_buy=1.045415
bot-1  | 2025/10/14 04:50:32 FLAT [pUp=0.14105, ema4=113065.89 vs ema8=113130.37, ema4_3rd=113188.58 vs ema8_3rd=113232.84]
bot-1  | 2025/10/14 04:50:32 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:32 [TRACE] equity: computed=1287.20998781 (ready=true)
bot-1  | 2025/10/14 04:50:33 TRACE price_fetch px=112993.37000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:33 [TICK] px=112993.37 lastClose(before-step)=112993.37
bot-1  | 2025/10/14 04:50:33 TRACE TARGET [TICK] px=112993.37 lastClose(before-step)=112993.37
bot-1  | 2025/10/14 04:50:33 TRACE step.start ts=2025-10-14T04:49:42Z price=112993.37000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:33 TRACE trail.raise lotSide=SELL trough=112993.37000000 stop=113445.34348000
bot-1  | 2025/10/14 04:50:33 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:33 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.13757, ema4=113063.24 vs ema8=113128.90, ema4_3rd=113188.58 vs ema8_3rd=113232.84, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:34 [DEBUG] EQUITY Trading: equityUSD=1287.21 lastAddEquitySell=1307.22 pct_diff_sell=0.984691 lastAddEquityBuy=1231.33 pct_diff_buy=1.045381
bot-1  | 2025/10/14 04:50:34 FLAT [pUp=0.13757, ema4=113063.24 vs ema8=113128.90, ema4_3rd=113188.58 vs ema8_3rd=113232.84]
bot-1  | 2025/10/14 04:50:34 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:35 [TRACE] equity: computed=1287.20998781 (ready=true)
bot-1  | 2025/10/14 04:50:36 TRACE price_fetch px=112974.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:36 [TICK] px=112974.00 lastClose(before-step)=112974.00
bot-1  | 2025/10/14 04:50:36 TRACE TARGET [TICK] px=112974.00 lastClose(before-step)=112974.00
bot-1  | 2025/10/14 04:50:36 TRACE step.start ts=2025-10-14T04:49:42Z price=112974.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:36 TRACE trail.raise lotSide=SELL trough=112974.00000000 stop=113425.89600000
bot-1  | 2025/10/14 04:50:36 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:36 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.12780, ema4=113055.49 vs ema8=113124.60, ema4_3rd=113188.58 vs ema8_3rd=113232.84, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:37 [DEBUG] EQUITY Trading: equityUSD=1287.21 lastAddEquitySell=1307.22 pct_diff_sell=0.984691 lastAddEquityBuy=1231.33 pct_diff_buy=1.045381
bot-1  | 2025/10/14 04:50:37 FLAT [pUp=0.12780, ema4=113055.49 vs ema8=113124.60, ema4_3rd=113188.58 vs ema8_3rd=113232.84]
bot-1  | 2025/10/14 04:50:37 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:37 [TRACE] equity: computed=1287.09326549 (ready=true)
bot-1  | 2025/10/14 04:50:38 TRACE price_fetch px=112963.41000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:38 [TICK] px=112963.41 lastClose(before-step)=112963.41
bot-1  | 2025/10/14 04:50:38 TRACE TARGET [TICK] px=112963.41 lastClose(before-step)=112963.41
bot-1  | 2025/10/14 04:50:38 TRACE step.start ts=2025-10-14T04:49:42Z price=112963.41000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:38 TRACE trail.raise lotSide=SELL trough=112963.41000000 stop=113415.26364000
bot-1  | 2025/10/14 04:50:38 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:38 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.12271, ema4=113051.25 vs ema8=113122.24, ema4_3rd=113188.58 vs ema8_3rd=113232.84, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:38 [DEBUG] EQUITY Trading: equityUSD=1287.09 lastAddEquitySell=1307.22 pct_diff_sell=0.984602 lastAddEquityBuy=1231.33 pct_diff_buy=1.045286
bot-1  | 2025/10/14 04:50:38 FLAT [pUp=0.12271, ema4=113051.25 vs ema8=113122.24, ema4_3rd=113188.58 vs ema8_3rd=113232.84]
bot-1  | 2025/10/14 04:50:38 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:43 [TRACE] equity: computed=1287.06538681 (ready=true)
bot-1  | 2025/10/14 04:50:44 TRACE price_fetch px=112963.40000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:44 [TICK] px=112963.40 lastClose(before-step)=112963.40
bot-1  | 2025/10/14 04:50:44 TRACE TARGET [TICK] px=112963.40 lastClose(before-step)=112963.40
bot-1  | 2025/10/14 04:50:44 TRACE step.start ts=2025-10-14T04:50:42Z price=112963.40000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:44 TRACE trail.raise lotSide=SELL trough=112963.40000000 stop=113415.25360000
bot-1  | 2025/10/14 04:50:44 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:44 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44566, ema4=113017.70 vs ema8=113088.09, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:44 [DEBUG] EQUITY Trading: equityUSD=1287.07 lastAddEquitySell=1307.22 pct_diff_sell=0.984581 lastAddEquityBuy=1231.33 pct_diff_buy=1.045263
bot-1  | 2025/10/14 04:50:44 FLAT [pUp=0.44566, ema4=113017.70 vs ema8=113088.09, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:44 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:44 [TRACE] equity: computed=1287.04567870 (ready=true)
bot-1  | 2025/10/14 04:50:45 TRACE price_fetch px=112942.13000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:45 [TICK] px=112942.13 lastClose(before-step)=112942.13
bot-1  | 2025/10/14 04:50:45 TRACE TARGET [TICK] px=112942.13 lastClose(before-step)=112942.13
bot-1  | 2025/10/14 04:50:45 TRACE step.start ts=2025-10-14T04:50:42Z price=112942.13000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:45 TRACE trail.raise lotSide=SELL trough=112942.13000000 stop=113393.89852000
bot-1  | 2025/10/14 04:50:45 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:45 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44441, ema4=113009.19 vs ema8=113083.36, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:46 [DEBUG] EQUITY Trading: equityUSD=1287.05 lastAddEquitySell=1307.22 pct_diff_sell=0.984566 lastAddEquityBuy=1231.33 pct_diff_buy=1.045247
bot-1  | 2025/10/14 04:50:46 FLAT [pUp=0.44441, ema4=113009.19 vs ema8=113083.36, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:46 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:49 [TRACE] equity: computed=1286.96940515 (ready=true)
bot-1  | 2025/10/14 04:50:50 TRACE price_fetch px=112938.08000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:50 [TICK] px=112938.08 lastClose(before-step)=112938.08
bot-1  | 2025/10/14 04:50:50 TRACE TARGET [TICK] px=112938.08 lastClose(before-step)=112938.08
bot-1  | 2025/10/14 04:50:50 TRACE step.start ts=2025-10-14T04:50:42Z price=112938.08000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:50 TRACE trail.raise lotSide=SELL trough=112938.08000000 stop=113389.83232000
bot-1  | 2025/10/14 04:50:50 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:50 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44416, ema4=113007.57 vs ema8=113082.46, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:50 [DEBUG] EQUITY Trading: equityUSD=1286.97 lastAddEquitySell=1307.22 pct_diff_sell=0.984507 lastAddEquityBuy=1231.33 pct_diff_buy=1.045185
bot-1  | 2025/10/14 04:50:50 FLAT [pUp=0.44416, ema4=113007.57 vs ema8=113082.46, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:50 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:50 [TRACE] equity: computed=1286.93200911 (ready=true)
bot-1  | 2025/10/14 04:50:51 TRACE price_fetch px=112933.89000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:51 [TICK] px=112933.89 lastClose(before-step)=112933.89
bot-1  | 2025/10/14 04:50:51 TRACE TARGET [TICK] px=112933.89 lastClose(before-step)=112933.89
bot-1  | 2025/10/14 04:50:51 TRACE step.start ts=2025-10-14T04:50:42Z price=112933.89000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:51 TRACE trail.raise lotSide=SELL trough=112933.89000000 stop=113385.62556000
bot-1  | 2025/10/14 04:50:51 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:51 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44390, ema4=113005.89 vs ema8=113081.53, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:51 [DEBUG] EQUITY Trading: equityUSD=1286.93 lastAddEquitySell=1307.22 pct_diff_sell=0.984479 lastAddEquityBuy=1231.33 pct_diff_buy=1.045155
bot-1  | 2025/10/14 04:50:51 FLAT [pUp=0.44390, ema4=113005.89 vs ema8=113081.53, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:51 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:51 [TRACE] equity: computed=1286.91319886 (ready=true)
bot-1  | 2025/10/14 04:50:52 TRACE price_fetch px=112925.89000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:52 [TICK] px=112925.89 lastClose(before-step)=112925.89
bot-1  | 2025/10/14 04:50:52 TRACE TARGET [TICK] px=112925.89 lastClose(before-step)=112925.89
bot-1  | 2025/10/14 04:50:52 TRACE step.start ts=2025-10-14T04:50:42Z price=112925.89000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:52 TRACE trail.raise lotSide=SELL trough=112925.89000000 stop=113377.59356000
bot-1  | 2025/10/14 04:50:52 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:52 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44339, ema4=113002.69 vs ema8=113079.75, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:53 [DEBUG] EQUITY Trading: equityUSD=1286.91 lastAddEquitySell=1307.22 pct_diff_sell=0.984464 lastAddEquityBuy=1231.33 pct_diff_buy=1.045140
bot-1  | 2025/10/14 04:50:53 FLAT [pUp=0.44339, ema4=113002.69 vs ema8=113079.75, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:53 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:53 [TRACE] equity: computed=1286.87728430 (ready=true)
bot-1  | 2025/10/14 04:50:54 TRACE price_fetch px=112925.04000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:54 [TICK] px=112925.04 lastClose(before-step)=112925.04
bot-1  | 2025/10/14 04:50:54 TRACE TARGET [TICK] px=112925.04 lastClose(before-step)=112925.04
bot-1  | 2025/10/14 04:50:54 TRACE step.start ts=2025-10-14T04:50:42Z price=112925.04000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:54 TRACE trail.raise lotSide=SELL trough=112925.04000000 stop=113376.74016000
bot-1  | 2025/10/14 04:50:54 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:54 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44334, ema4=113002.35 vs ema8=113079.56, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:54 [DEBUG] EQUITY Trading: equityUSD=1286.88 lastAddEquitySell=1307.22 pct_diff_sell=0.984437 lastAddEquityBuy=1231.33 pct_diff_buy=1.045111
bot-1  | 2025/10/14 04:50:54 FLAT [pUp=0.44334, ema4=113002.35 vs ema8=113079.56, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:54 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:56 [TRACE] equity: computed=1286.88675677 (ready=true)
bot-1  | 2025/10/14 04:50:57 TRACE price_fetch px=112892.74000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:50:57 [TICK] px=112892.74 lastClose(before-step)=112892.74
bot-1  | 2025/10/14 04:50:57 TRACE TARGET [TICK] px=112892.74 lastClose(before-step)=112892.74
bot-1  | 2025/10/14 04:50:57 TRACE step.start ts=2025-10-14T04:50:42Z price=112892.74000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:50:57 TRACE trail.raise lotSide=SELL trough=112892.74000000 stop=113344.31096000
bot-1  | 2025/10/14 04:50:57 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:50:57 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44117, ema4=112989.43 vs ema8=113072.38, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:50:57 [DEBUG] EQUITY Trading: equityUSD=1286.89 lastAddEquitySell=1307.22 pct_diff_sell=0.984444 lastAddEquityBuy=1231.33 pct_diff_buy=1.045118
bot-1  | 2025/10/14 04:50:57 FLAT [pUp=0.44117, ema4=112989.43 vs ema8=113072.38, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:50:57 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:50:59 [TRACE] equity: computed=1286.84186357 (ready=true)
bot-1  | 2025/10/14 04:51:00 TRACE price_fetch px=112892.00000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:51:00 [TICK] px=112892.00 lastClose(before-step)=112892.00
bot-1  | 2025/10/14 04:51:00 TRACE TARGET [TICK] px=112892.00 lastClose(before-step)=112892.00
bot-1  | 2025/10/14 04:51:00 TRACE step.start ts=2025-10-14T04:50:42Z price=112892.00000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:51:00 TRACE trail.raise lotSide=SELL trough=112892.00000000 stop=113343.56800000
bot-1  | 2025/10/14 04:51:00 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:51:00 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44111, ema4=112989.14 vs ema8=113072.22, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:51:00 [DEBUG] EQUITY Trading: equityUSD=1286.84 lastAddEquitySell=1307.22 pct_diff_sell=0.984410 lastAddEquityBuy=1231.33 pct_diff_buy=1.045082
bot-1  | 2025/10/14 04:51:00 FLAT [pUp=0.44111, ema4=112989.14 vs ema8=113072.22, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:51:00 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:51:38 [TRACE] equity: computed=1286.84186357 (ready=true)
bot-1  | 2025/10/14 04:51:39 TRACE price_fetch px=112888.01000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:51:39 [TICK] px=112888.01 lastClose(before-step)=112888.01
bot-1  | 2025/10/14 04:51:39 TRACE TARGET [TICK] px=112888.01 lastClose(before-step)=112888.01
bot-1  | 2025/10/14 04:51:39 TRACE step.start ts=2025-10-14T04:50:42Z price=112888.01000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:51:39 TRACE trail.raise lotSide=SELL trough=112888.01000000 stop=113339.56204000
bot-1  | 2025/10/14 04:51:39 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:51:39 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44083, ema4=112987.54 vs ema8=113071.33, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:51:39 [DEBUG] EQUITY Trading: equityUSD=1286.84 lastAddEquitySell=1307.22 pct_diff_sell=0.984410 lastAddEquityBuy=1231.33 pct_diff_buy=1.045082
bot-1  | 2025/10/14 04:51:39 FLAT [pUp=0.44083, ema4=112987.54 vs ema8=113071.33, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:51:39 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:51:39 [TRACE] equity: computed=1286.70722886 (ready=true)
bot-1  | 2025/10/14 04:51:40 TRACE price_fetch px=112884.04000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:51:40 [TICK] px=112884.04 lastClose(before-step)=112884.04
bot-1  | 2025/10/14 04:51:40 TRACE TARGET [TICK] px=112884.04 lastClose(before-step)=112884.04
bot-1  | 2025/10/14 04:51:40 TRACE step.start ts=2025-10-14T04:50:42Z price=112884.04000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:51:40 TRACE trail.raise lotSide=SELL trough=112884.04000000 stop=113335.57616000
bot-1  | 2025/10/14 04:51:40 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:51:40 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.44055, ema4=112985.95 vs ema8=113070.45, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:51:41 [DEBUG] EQUITY Trading: equityUSD=1286.71 lastAddEquitySell=1307.22 pct_diff_sell=0.984307 lastAddEquityBuy=1231.33 pct_diff_buy=1.044972
bot-1  | 2025/10/14 04:51:41 FLAT [pUp=0.44055, ema4=112985.95 vs ema8=113070.45, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:51:41 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:51:41 [TRACE] equity: computed=1286.68940626 (ready=true)
bot-1  | 2025/10/14 04:51:42 TRACE price_fetch px=112864.03000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:51:42 [TICK] px=112864.03 lastClose(before-step)=112864.03
bot-1  | 2025/10/14 04:51:42 TRACE TARGET [TICK] px=112864.03 lastClose(before-step)=112864.03
bot-1  | 2025/10/14 04:51:42 TRACE step.start ts=2025-10-14T04:50:42Z price=112864.03000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:51:42 TRACE trail.raise lotSide=SELL trough=112864.03000000 stop=113315.48612000
bot-1  | 2025/10/14 04:51:42 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:51:42 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.43908, ema4=112977.95 vs ema8=113066.00, ema4_3rd=113148.36 vs ema8_3rd=113200.66, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:51:42 [DEBUG] EQUITY Trading: equityUSD=1286.69 lastAddEquitySell=1307.22 pct_diff_sell=0.984293 lastAddEquityBuy=1231.33 pct_diff_buy=1.044958
bot-1  | 2025/10/14 04:51:42 FLAT [pUp=0.43908, ema4=112977.95 vs ema8=113066.00, ema4_3rd=113148.36 vs ema8_3rd=113200.66]
bot-1  | 2025/10/14 04:51:42 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 04:51:43 [SYNC] latest=2025-10-14 04:51:43.749004798 +0000 UTC history_last=2025-10-14 04:51:43.749004798 +0000 UTC len=6000
bot-1  | 2025/10/14 04:51:43 TRACE price_fetch px=112855.99000000 stale=false err=<nil>
bot-1  | 2025/10/14 04:51:43 [TICK] px=112855.99 lastClose(before-step)=112855.99
bot-1  | 2025/10/14 04:51:43 TRACE TARGET [TICK] px=112855.99 lastClose(before-step)=112855.99
bot-1  | 2025/10/14 04:51:43 TRACE step.start ts=2025-10-14T04:51:43Z price=112855.99000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 04:51:43 TRACE trail.raise lotSide=SELL trough=112855.99000000 stop=113307.41396000
bot-1  | 2025/10/14 04:51:43 [DEBUG] Nearest Take (to close a buy lot)=123695.66, (to close a sell lot)=109854.49, across 9 BuyLots, and 1 SellLots
bot-1  | 2025/10/14 04:51:43 [DEBUG] Total Lots=10, Decision=FLAT Reason = pUp=0.48053, ema4=112929.16 vs ema8=113019.33, ema4_3rd=113109.81 vs ema8_3rd=113167.62, buyThresh=0.520, sellThresh=0.470, LongOnly=false
bot-1  | 2025/10/14 04:51:44 [DEBUG] EQUITY Trading: equityUSD=1286.60 lastAddEquitySell=1307.22 pct_diff_sell=0.984225 lastAddEquityBuy=1231.33 pct_diff_buy=1.044885
bot-1  | 2025/10/14 04:51:44 FLAT [pUp=0.48053, ema4=112929.16 vs ema8=113019.33, ema4_3rd=113109.81 vs ema8_3rd=113167.62]
bot-1  | 2025/10/14 04:51:44 [TRACE] equity: calling http://bridge:8787/accounts?limit=250
--
bot-1  | 2025/10/14 05:06:43 [TRACE] equity: computed=1288.42210421 (ready=true)
bot-1  | 2025/10/14 05:06:44 TRACE price_fetch px=113308.22000000 stale=false err=<nil>
bot-1  | 2025/10/14 05:06:44 [TICK] px=113308.22 lastClose(before-step)=113308.22
bot-1  | 2025/10/14 05:06:44 TRACE TARGET [TICK] px=113308.22 lastClose(before-step)=113308.22
bot-1  | 2025/10/14 05:06:44 TRACE step.start ts=2025-10-14T05:05:57Z price=113308.22000000 lotsBuy=9 lotsSell=1 lastAddBuy=2025-10-12T15:22:45Z lastAddSell=2025-10-12T22:00:50Z winLowBuy=112482.94000000 winHighSell=116077.51000000 latchedGateBuy=0.00000000 latchedGateSell=0.00000000
bot-1  | 2025/10/14 05:06:44 TRACE trail.trigger lotSide=SELL price=113308.22000000 stop=113307.41396000
bot-1  | 2025/10/14 05:06:44 TRACE order.close request side=BUY baseReq=0.00620572 quoteEst=703.16 priceSnap=113308.22000000
bot-1  | 2025/10/14 05:06:45 TRACE order.close placed price=113314.95000000 baseFilled=0.00613175 quoteSpent=694.82 fee=8.3378
bot-1  | 2025/10/14 05:06:45 [WARN] partial fill (exit): requested_base=0.00620572 filled_base=0.00613175 (98.81%)
bot-1  | 2025/10/14 05:06:45 TRACE fill.exit partial requested=0.00620572 filled=0.00613175
bot-1  | 2025/10/14 05:06:45 TRACE exit.classify side=SELL kind=runner reason=trailing_stop open=115636.31000000 exec=113314.95000000 baseFilled=0.00613175 rawPL=14.233999 entryFee=8.611279 exitFee=8.337827 finalPL=-2.715107
chidi@localhost:~/coinbase/monitoring$


If you want to turn that small fee drag into positive net:

Prefer maker adds: make sure new entries are post-only (your /order/limit_post_only route exists now—wire your “open” path to use it). Maker fees are much lower and often zero on some tiers.

Fee-aware take profits: widen the take targets (or dynamic min-spread) so expected gross > (entry+exit fees) + slip buffer. Right now you’re capturing spreads that are profitable pre-fees but not post-fees.

Size vs. fees: if your exchange fee is mostly proportional with a minimum tick, consider slightly larger lot size so per-trade fixed/rounded fee impact shrinks (within your risk bounds).

Stop style: trailing stops will almost always taker. That’s fine for exits, but try to ensure entries are maker, so the round-trip average fee falls.

Lot cap behavior: you hit the 8-lot cap several times while signals were BUY-favorable—decide whether to:

raise the cap, or

add a rule to free a worst lot / rebalance when strong signal persists but cap is hit.

If you want, I can sketch the exact changes to your Go side to call POST /order/limit_post_only for entries and keep the current market/taker path for stops/exits.


=========================================

Here’s how I’d tune it so it helps more than it hurts:

Don’t arm the trail until the trade is “worth closing”

Gate by profit: only trail.activate after unrealized P&L ≥ (entry fee + estimated exit fee + buffer).

In your example, raw profit was ~$14 and fees were ~$17 — the trail should not have been active yet.

Make the trail volatility-aware

Use trail_offset = max(k × ATR, pct × price) (e.g., 1.5×ATR(14) or 0.35%).

Your stop kept ratcheting up very close to price and got tapped on the rebound.

Lock to breakeven first, then trail tighter

Phase A: once P&L ≥ fees, move stop to breakeven + tiny buffer.

Phase B: after P&L ≥ target1 (say +0.3% or +1×ATR), start a looser trail.

Phase C: once P&L ≥ target2 (say +0.6% or +2×ATR), tighten the trail.

Prefer proactive take-profits (maker) over reactive stop exits

For shorts, you can place a resting buy limit below market at your TP — that often fills as maker (much cheaper).

Keep a wider trailing (or hard) stop only as a backstop. Stops that trigger market buys will nearly always be taker (expensive) on rebounds.

Be fee-aware everywhere

Log an “expected exit fee” using recent fills and require expected_gross ≥ exit_fee × X before enabling trail.

If possible, improve your Coinbase fee tier; 1%+ taker makes tight trailing unworkable.

Reduce whipsaw with a minimum re-arm distance/time

After a raise, require either Δprice ≥ y × ATR or t ≥ N bars before the trail can ratchet again.