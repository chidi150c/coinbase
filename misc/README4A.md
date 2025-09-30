# From ~/coinbase/monitoring
docker compose config | sed -n '/bridge_binance:/,/^[^ ]/p'

chidi@localhost:~/coinbase/monitoring$ jq '{lots: [.Lots[] | {side: .Side, base: .SizeBase, open: .OpenPrice, stop: .Stop, take: .Take, opened: .OpenTime, reason: (.reason // "")}],
     count: (.Lots|length)}' /opt/coinbase/state/bot_state.coinbase.json
{
  "lots": [
    {
      "side": "BUY",
      "base": 0.00023746,
      "open": 115807.8,
      "stop": -1042270.2000000001,
      "take": 120208.4964,
      "opened": "2025-09-14T03:39:14.11262726Z",
      "reason": ""
    },
    {
      "side": "BUY",
      "base": 0.00023726,
      "open": 115780.09,
      "stop": -1042064.64,
      "take": 117984.87423999999,
      "opened": "2025-09-14T06:55:05.532823184Z",
      "reason": ""
    },
    {
      "side": "BUY",
      "base": 0.00023688,
      "open": 114653.08,
      "stop": -1031958,
      "take": 116713.71582498093,
      "opened": "2025-09-22T00:51:51.245345656Z",
      "reason": "pUp=0.71246|gatePrice=114708.000|latched=114708.000|effPct=0.400|basePct=1.500|elapsedHr=152.3|PriceDownGoingUp=true|LowBottom=false"
    },
    {
      "side": "BUY",
      "base": 0.0002007546095251655,
      "open": 112376,
      "stop": -1011384,
      "take": 114427.42935945376,
      "opened": "2025-09-22T06:04:39.575039171Z",
      "reason": "pUp=0.82707|gatePrice=114207.570|latched=114207.570|effPct=0.400|basePct=1.500|elapsedHr=5.2|PriceDownGoingUp=true|LowBottom=false"
    },
    {
      "side": "BUY",
      "base": 0.00023712,
      "open": 112594.15,
      "stop": -1013347.35,
      "take": 114529.87169935463,
      "opened": "2025-09-22T13:45:23.744104998Z",
      "reason": "pUp=0.61676|gatePrice=112594.570|latched=112594.570|effPct=0.400|basePct=1.500|elapsedHr=2.9|PriceDownGoingUp=true|LowBottom=false"
    },
    {
      "side": "SELL",
      "base": 0.00024241,
      "open": 109538,
      "stop": 1204874,
      "take": 107688.17409099058,
      "opened": "2025-09-26T00:55:47.23421954Z",
      "reason": "pUp=0.01274|gatePrice=109502.605|latched=0.000|effPct=0.400|basePct=1.500|elapsedHr=1.2|HighPeak=false|PriceUpGoingDown=true"
    },
    {
      "side": "SELL",
      "base": 0.00024186,
      "open": 109612.66,
      "stop": 1205685.25,
      "take": 107854.026,
      "opened": "2025-09-26T07:13:19.91336582Z",
      "reason": "pUp=0.43109|gatePrice=109592.724|latched=0.000|effPct=0.400|basePct=1.500|elapsedHr=1.4|HighPeak=true|PriceUpGoingDown=false"
    },
    {
      "side": "SELL",
      "base": 0.00027455,
      "open": 109327.99,
      "stop": 1202190,
      "take": 107541.36,
      "opened": "2025-09-26T12:31:22.274164002Z",
      "reason": "pUp=0.34468|gatePrice=109286.780|latched=109286.780|effPct=0.400|basePct=1.500|elapsedHr=2.2|HighPeak=true|PriceUpGoingDown=false"
    },
    {
      "side": "SELL",
      "base": 0.00027392,
      "open": 109851.4,
      "stop": 1208350.33,
      "take": 108092.42951999999,
      "opened": "2025-09-26T17:32:30.039910768Z",
      "reason": "pUp=0.43042|gatePrice=109850.000|latched=109850.000|effPct=0.400|basePct=1.500|elapsedHr=5.0|HighPeak=false|PriceUpGoingDown=true"
    },
    {
      "side": "SELL",
      "base": 0.0002727,
      "open": 110925.96,
      "stop": 1220185.67,
      "take": 109151.15448,
      "opened": "2025-09-28T20:38:11.416595924Z",
      "reason": "pUp=0.37770|gatePrice=110229.610|latched=110229.610|effPct=0.400|basePct=1.500|elapsedHr=51.1|HighPeak=false|PriceUpGoingDown=true"
    }
  ],
  "count": 10
}
