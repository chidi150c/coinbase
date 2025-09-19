#!/usr/bin/env python3
import os, asyncio, json, time
from typing import Dict, List, Optional

from fastapi import FastAPI, Query
from fastapi.responses import JSONResponse
import uvicorn
import websockets

EXCHANGE = os.getenv("EXCHANGE", "hitbtc").strip().lower()   # "binance" | "hitbtc"
SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8788"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))                # mark stale if older than 3s

# In-memory state
last_price: Optional[float] = None
last_ts_ms: Optional[int] = None

# Minute-candle aggregator: minute_unix -> [o,h,l,c,vol]
candles: Dict[int, List[float]] = {}
MAX_MINUTES = 7 * 24 * 60

def _now_ms() -> int:
    return int(time.time() * 1000)

def _minute_start(ts_ms: int) -> int:
    return (ts_ms // 60000) * 60

def _trim_old_candles() -> None:
    if len(candles) <= MAX_MINUTES:
        return
    for k in sorted(candles.keys())[: len(candles) - MAX_MINUTES]:
        candles.pop(k, None)

def _update_candle(px: float, ts_ms: int, vol: float = 0.0) -> None:
    m = _minute_start(ts_ms)
    if m not in candles:
        candles[m] = [px, px, px, px, vol]  # o,h,l,c,vol
    else:
        o, h, l, c, v = candles[m]
        if px > h: h = px
        if px < l: l = px
        c = px
        v += vol
        candles[m] = [o, h, l, c, v]
    _trim_old_candles()

async def _ws_loop_binance():
    """
    Binance: bookTicker midprice (bid+ask)/2
    wss://stream.binance.com:9443/ws/<symbol_lower>@bookTicker
    """
    global last_price, last_ts_ms
    url = f"wss://stream.binance.com:9443/ws/{SYMBOL.lower()}@bookTicker"
    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=15, ping_timeout=15, max_queue=1024) as ws:
                backoff = 1
                while True:
                    msg = await ws.recv()
                    data = json.loads(msg)
                    b = float(data.get("b") or 0)
                    a = float(data.get("a") or 0)
                    if b > 0 and a > 0:
                        mid = (a + b) / 2.0
                        ts = int(data.get("E") or _now_ms())
                        last_price, last_ts_ms = mid, ts
                        _update_candle(mid, ts, 0.0)
        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 15)

def _split_symbol(sym: str) -> (str, str):
    """Guess BASE-QUOTE for HitBTC (common suffixes)."""
    s = sym.upper()
    suffixes = ["USDT", "USDC", "USD", "BTC", "ETH", "EUR"]
    for suf in suffixes:
        if s.endswith(suf) and len(s) > len(suf):
            return s[:-len(suf)], suf
    # fallback
    return s, "USDT"

# --- replace your _ws_loop_hitbtc() with this minimal version ---
async def _ws_loop_hitbtc():
    """
    HitBTC public WS: subscribe to *both* ticker/price/1s and ticker/1s.
    Use last price 'c' when available; otherwise mid of best bid/ask from ticker/1s.
    Endpoint: wss://api.hitbtc.com/api/3/ws/public
    """
    global last_price, last_ts_ms
    url = "wss://api.hitbtc.com/api/3/ws/public"

    subs = [
        {
            "method": "subscribe",
            "ch": "ticker/price/1s",
            "params": {"symbols": [SYMBOL]},
            "id": 1,
        },
        {
            "method": "subscribe",
            "ch": "ticker/1s",
            "params": {"symbols": [SYMBOL]},
            "id": 2,
        },
    ]

    backoff = 1
    while True:
        try:
            # HitBTC pings every ~30s; websockets handles control frames
            async with websockets.connect(
                url, ping_interval=25, ping_timeout=25, max_queue=1024
            ) as ws:
                # subscribe to both feeds
                for s in subs:
                    await ws.send(json.dumps(s))
                backoff = 1

                while True:
                    msg = await ws.recv()
                    try:
                        data = json.loads(msg)
                    except Exception:
                        continue

                    if not isinstance(data, dict):
                        continue

                    ch = data.get("ch")
                    d = data.get("data")
                    if not d or ch not in ("ticker/price/1s", "ticker/1s"):
                        continue

                    payload = d.get(SYMBOL) or {}
                    # prefer exchange-provided timestamp; fallback to now
                    ts = int(payload.get("t") or _now_ms())

                    # price source priority:
                    # 1) 'c' from either channel (last price)
                    # 2) mid of best a/b from ticker/1s when 'c' absent
                    px = None
                    c = payload.get("c")
                    if c is not None:
                        try:
                            px = float(c)
                        except Exception:
                            px = None

                    if px is None and ch == "ticker/1s":
                        try:
                            a = float(payload.get("a") or 0)
                            b = float(payload.get("b") or 0)
                            if a > 0 and b > 0:
                                px = (a + b) / 2.0
                        except Exception:
                            px = None

                    if px is not None and px > 0:
                        last_price, last_ts_ms = px, ts
                        _update_candle(px, ts, 0.0)

        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 15)

    """
    HitBTC public WS: subscribe to multiple feeds and use whichever updates first:
      - orderbook/top/1000ms (mid = (bid+ask)/2)
      - ticker/price/1s      (last price 'c')
      - price/rate/1s        (converted rate for BASE→QUOTE)
    Endpoint: wss://api.hitbtc.com/api/3/ws/public
    """
    global last_price, last_ts_ms
    url = "wss://api.hitbtc.com/api/3/ws/public"
    base, quote = _split_symbol(SYMBOL)

    subs = [
        {
            "method": "subscribe",
            "ch": "orderbook/top/1000ms",
            "params": {"symbols": [SYMBOL]},
            "id": 1,
        },
        {
            "method": "subscribe",
            "ch": "ticker/price/1s",
            "params": {"symbols": [SYMBOL]},
            "id": 2,
        },
        {
            "method": "subscribe",
            "ch": "price/rate/1s",
            "params": {"currencies": [base], "target_currency": quote},
            "id": 3,
        },
    ]

    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=25, ping_timeout=25, max_queue=1024) as ws:
                for s in subs:
                    await ws.send(json.dumps(s))
                backoff = 1

                while True:
                    raw = await ws.recv()
                    try:
                        data = json.loads(raw)
                    except Exception:
                        continue

                    ch = data.get("ch")

                    # --- orderbook/top/1000ms: mid from bid/ask ---
                    if ch == "orderbook/top/1000ms" and "data" in data:
                        row = (data["data"] or {}).get(SYMBOL) or {}
                        a = row.get("a"); b = row.get("b")
                        if a is not None and b is not None:
                            try:
                                ask = float(a); bid = float(b)
                                if ask > 0 and bid > 0:
                                    mid = (ask + bid) / 2.0
                                    ts = int(row.get("t") or _now_ms())  # ms
                                    last_price, last_ts_ms = mid, ts
                                    _update_candle(mid, ts, 0.0)
                            except Exception:
                                pass
                        continue

                    # --- ticker/price/1s: last 'c' ---
                    if ch == "ticker/price/1s" and "data" in data:
                        payload = (data["data"] or {}).get(SYMBOL) or {}
                        c = payload.get("c"); t = payload.get("t")
                        if c is not None:
                            try:
                                px = float(c)
                                ts = int(t or _now_ms())
                                last_price, last_ts_ms = px, ts
                                _update_candle(px, ts, 0.0)
                            except Exception:
                                pass
                        continue

                    # --- price/rate/1s: data -> {BASE: { t, r }} ---
                    if ch == "price/rate/1s" and "data" in data:
                        cur = (data["data"] or {}).get(base) or {}
                        r = cur.get("r"); t = cur.get("t")
                        if r is not None:
                            try:
                                px = float(r)
                                ts = int(t or _now_ms())
                                last_price, last_ts_ms = px, ts
                                _update_candle(px, ts, 0.0)
                            except Exception:
                                pass
                        continue

                    # ignore other responses (subscription acks, etc.)

        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 15)

# ---- HTTP app ----
app = FastAPI()

@app.get("/health")
async def health():
    age_ms = None
    stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms() - last_ts_ms)
        stale = age_ms > STALE_MS
    return JSONResponse({
        "ok": True,
        "exchange": EXCHANGE,
        "symbol": SYMBOL,
        "price": last_price,
        "age_ms": age_ms,
        "stale": stale,
    })

@app.get("/price")
async def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None
    stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms() - last_ts_ms)
        stale = age_ms > STALE_MS
    return JSONResponse({
        "product_id": product_id,
        "price": float(last_price) if last_price is not None else 0.0,
        "ts": last_ts_ms,
        "stale": stale,
    })

@app.get("/candles")
async def candles_endpoint(
    product_id: str,
    granularity: str = Query(default="ONE_MINUTE"),
    limit: int = Query(default=350, ge=1, le=1000),
    start: Optional[int] = Query(default=None),  # unix seconds
    end: Optional[int] = Query(default=None),    # unix seconds
):
    g = (granularity or "").upper()
    if g != "ONE_MINUTE":
        return JSONResponse({"candles":[]})
    all_minutes = sorted(candles.keys())
    if not all_minutes:
        return JSONResponse({"candles":[]})

    if end is None:
        end = int(time.time())
    if start is None:
        start = end - (limit * 60) - 5

    out = []
    count = 0
    for m in all_minutes:
        if m < start or m > end:
            continue
        o,h,l,c,v = candles[m]
        out.append({
            "start": str(m),
            "open":  f"{o}",
            "high":  f"{h}",
            "low":   f"{l}",
            "close": f"{c}",
            "volume": f"{v}",
        })
        count += 1
        if count >= limit:
            break

    return JSONResponse({"candles": out})

async def _runner():
    if EXCHANGE == "binance":
        task = asyncio.create_task(_ws_loop_binance())
    elif EXCHANGE == "hitbtc":
        task = asyncio.create_task(_ws_loop_hitbtc())
    else:
        raise RuntimeError(f"Unsupported EXCHANGE={EXCHANGE}")
    config = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    server = uvicorn.Server(config)
    await asyncio.gather(task, server.serve())

if __name__ == "__main__":
    try:
        asyncio.run(_runner())
    except KeyboardInterrupt:
        pass
