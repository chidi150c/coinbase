#!/usr/bin/env python3
import os, asyncio, json, time, signal, math
from typing import Dict, List, Tuple, Optional
from collections import defaultdict

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
# We’ll keep ~1 week of minutes to be generous.
candles: Dict[int, List[float]] = {}
MAX_MINUTES = 7 * 24 * 60

def _now_ms() -> int:
    return int(time.time() * 1000)

def _minute_start(ts_ms: int) -> int:
    return (ts_ms // 60000) * 60

def _trim_old_candles() -> None:
    if len(candles) <= MAX_MINUTES:
        return
    # drop oldest (by key)
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
    Binance market data stream: bookTicker => use midprice (bid+ask)/2
    WS endpoint: wss://stream.binance.com:9443/ws/<symbol_lower>@bookTicker
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
                    # Expect fields: b (best bid price), a (best ask price), E (event time)
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

async def _ws_loop_hitbtc():
    """
    HitBTC public WS: subscribe to orderbook/top/1000ms
    Endpoint: wss://api.hitbtc.com/api/3/ws/public
    Subscription:
      {
        "method":"subscribe",
        "ch":"orderbook/top/1000ms",
        "params":{"symbols":["BTCUSDT"]},
        "id":1
      }
    Data arrives as:
      {"ch":"orderbook/top/1000ms","data":{"BTCUSDT":{"t":..., "a":"<ask>", "b":"<bid>", ...}}}
    We publish mid = (bid+ask)/2.
    """
    global last_price, last_ts_ms
    url = "wss://api.hitbtc.com/api/3/ws/public"
    sub = {
        "method":"subscribe",
        "ch":"orderbook/top/1000ms",
        "params":{"symbols":[SYMBOL]},
        "id": 1
    }
    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=25, ping_timeout=25, max_queue=1024) as ws:
                await ws.send(json.dumps(sub))
                backoff = 1
                while True:
                    msg = await ws.recv()
                    try:
                        data = json.loads(msg)
                    except Exception:
                        continue

                    # Successful subscription returns {"result":{...},"id":1}
                    # Updates we want:
                    # {"ch":"orderbook/top/1000ms","data":{"BTCUSDT":{"t":<ms>,"a":"...","A":"...","b":"...","B":"..."}}}
                    if data.get("ch") == "orderbook/top/1000ms" and "data" in data:
                        row = (data["data"] or {}).get(SYMBOL) or {}
                        a = row.get("a")
                        b = row.get("b")
                        if a is None or b is None:
                            continue
                        try:
                            ask = float(a)
                            bid = float(b)
                            if ask > 0 and bid > 0:
                                mid = (ask + bid) / 2.0
                                ts = int(row.get("t") or _now_ms())  # ms
                                last_price, last_ts_ms = mid, ts
                                _update_candle(mid, ts, 0.0)
                        except Exception:
                            pass
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
    # accept any product_id; we serve the single configured symbol
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
        # Keep it simple: we only expose M1 from the tick aggregator
        return JSONResponse({"candles":[]})
    # Build list of minutes in range
    all_minutes = sorted(candles.keys())
    if not all_minutes:
        return JSONResponse({"candles":[]})

    if end is None:
        end = int(time.time())
    if start is None:
        # If no start provided, use the last `limit` minutes up to end
        start = end - (limit * 60) - 5

    out = []
    count = 0
    for m in all_minutes:
        if m < start or m > end:
            continue
        o,h,l,c,v = candles[m]
        out.append({
            "start": str(m),         # unix seconds as string
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
    # choose the right WS loop
    if EXCHANGE == "binance":
        task = asyncio.create_task(_ws_loop_binance())
    elif EXCHANGE == "hitbtc":
        task = asyncio.create_task(_ws_loop_hitbtc())
    else:
        raise RuntimeError(f"Unsupported EXCHANGE={EXCHANGE}")
    # also run HTTP
    config = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    server = uvicorn.Server(config)
    await asyncio.gather(task, server.serve())

if __name__ == "__main__":
    try:
        asyncio.run(_runner())
    except KeyboardInterrupt:
        pass
