#!/usr/bin/env python3 
# FILE: bridge_hitbtc/ws_hitbtc.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using HitBTC WS/REST (binance-style candles contract).
import os, asyncio, json, time, hmac, hashlib, logging
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq, parse as urlparse
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path
import websockets
import uvicorn

# ---- Logging ----
log = logging.getLogger("bridge-hitbtc")
logging.basicConfig(level=logging.INFO)

# ---- Config (mirror coinbase .env knobs where applicable) ----
SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8788"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

# HitBTC creds are not required for public data, but keep envs for parity
HITBTC_API_KEY     = os.getenv("HITBTC_API_KEY", "").strip()
HITBTC_API_SECRET  = os.getenv("HITBTC_API_SECRET", "").strip()
HITBTC_BASE_URL    = os.getenv("HITBTC_BASE_URL", "https://api.hitbtc.com").strip().rstrip("/")

# ---- In-memory state ----
last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}  # minuteStartMs -> [o,h,l,c,vol]
_first_tick_logged: bool = False  # TRACE: first-tick marker

# ---- Helpers ----
def _now_ms() -> int: return int(time.time() * 1000)
def _now_iso() -> str: return datetime.utcnow().replace(tzinfo=timezone.utc).isoformat()
def _minute_start(ts_ms: int) -> int: return (ts_ms // 60000) * 60000

def _trim_old_candles(max_minutes: int = 6000):
    if len(candles) <= max_minutes: return
    for k in sorted(candles.keys())[:-max_minutes]:
        candles.pop(k, None)

def _update_candle(px: float, ts_ms: int, vol: float = 0.0):
    m = _minute_start(ts_ms)
    if m not in candles:
        candles[m] = [px, px, px, px, vol]
    else:
        o,h,l,c,v = candles[m]
        if px > h: h = px
        if px < l: l = px
        candles[m] = [o,h,l,px,v+vol]
    _trim_old_candles()

def _normalize_symbol(pid: str) -> str:
    s = (pid or SYMBOL).upper().replace("-", "")
    # Map BTC-USD style to spot convention BTCUSDT if needed
    if s.endswith("USD") and not s.endswith("USDT"):
        return s + "T"
    return s

def _http_get(url: str, headers: Optional[Dict[str,str]] = None):
    req = urlreq.Request(url, headers=headers or {})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8", "ignore")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON")

def _split_product(pid: str) -> Tuple[str,str]:
    p = pid.upper().replace("-", "")
    for q in ["FDUSD","USDT","USDC","BUSD","TUSD","EUR","GBP","TRY","BRL","BTC","ETH","BNB","USD"]:
        if p.endswith(q) and len(p) > len(q): return p[:-len(q)], q
    if len(p) > 3: return p[:-3], p[-3:]
    return p, "USD"

def _sum_available(accts: List[Dict], asset: str) -> str:
    asset = asset.upper()
    for a in accts:
        if a.get("currency","").upper() == asset:
            return str(a.get("available_balance",{}).get("value","0"))
    return "0"

# ---- WS consumer (HitBTC trades) ----
async def _ws_loop():
    """Consume HitBTC public trades and synthesize midprice and minute candles.

    HitBTC WS (v3) public trades: wss://api.hitbtc.com/api/3/ws/public
    We'll subscribe to trades.{SYMBOL} and compute candles locally.
    """
    global last_price, last_ts_ms, _first_tick_logged
    sym = _normalize_symbol(SYMBOL)
    ws_url = "wss://api.hitbtc.com/api/3/ws/public"
    sub_msg = json.dumps({
        "method": "subscribe",
        "ch": f"trades/{sym}",
        "params": {"symbols": [sym]}
    })
    log.info(f"[WS] connecting url={ws_url} channel=trades/{sym}")
    backoff = 1
    attempt = 0
    while True:
        attempt += 1
        try:
            log.info(f"[WS] connect attempt={attempt}")
            async with websockets.connect(ws_url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
                await ws.send(sub_msg)
                log.info(f"[WS] connected and subscribed to trades/{sym}")
                backoff = 1
                while True:
                    raw = await ws.recv()
                    ts = _now_ms()
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        continue
                    # HitBTC trade payloads can be nested; try to extract price
                    px = None
                    d = msg.get("result") or msg.get("data") or {}
                    # Accept both array-of-trades and single trade
                    trades = d if isinstance(d, list) else d.get(sym) or d.get("trades") or []
                    if isinstance(trades, dict):  # sometimes keyed by symbol
                        trades = trades.get("trades") or trades.get("t") or []
                    if isinstance(trades, list) and trades:
                        t0 = trades[-1]
                        p = t0.get("price") or t0.get("p")
                        try:
                            if p is not None:
                                px = float(p)
                        except Exception:
                            px = None
                    if px and px > 0:
                        last_price = px
                        last_ts_ms = ts
                        _update_candle(px, ts)
                        if not _first_tick_logged:
                            _first_tick_logged = True
                            log.info(f"[WS] first tick px={px:.8f} ts_ms={ts}")
        except Exception as e:
            log.warning(f"[WS] disconnect err={e} backoff={backoff}s")
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

# ---- FastAPI app ----
app = FastAPI(title="bridge-hitbtc", version="0.3")

# Map Coinbase granularities → binance-style intervals we reuse for query shape
GRAN_MAP = {
    "ONE_MINUTE": "1m",
    "FIVE_MINUTE": "5m",
    "FIFTEEN_MINUTE": "15m",
    "THIRTY_MINUTE": "30m",
    "ONE_HOUR": "1h",
    "TWO_HOUR": "2h",
    "FOUR_HOUR": "4h",
    "SIX_HOUR": "6h",
    "ONE_DAY": "1d",
}
_INTERVAL_TO_GRAN = {v: k for k, v in GRAN_MAP.items()}

@app.get("/health")
def health(): return {"ok": True}

@app.get("/price")
def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None; stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms()-last_ts_ms)
        stale  = age_ms > STALE_MS
    return {"product_id": product_id, "price": float(last_price) if last_price else 0.0, "ts": last_ts_ms, "stale": stale}

def _interval_to_hitbtc(interval: str) -> str:
    """Translate binance-like intervals to HitBTC intervals."""
    m = {
        "1m": "M1", "3m": "M3", "5m": "M5", "15m": "M15", "30m": "M30",
        "1h": "H1", "4h": "H4", "6h": "H6", "12h": "H12",
        "1d": "D1"
    }
    return m.get(interval, "M1")

@app.get("/candles")
def get_candles(
    # legacy names (ignored for HitBTC-specific handler)
    product_id: str = Query(default=SYMBOL),
    granularity: str = Query(default="ONE_MINUTE"),
    start: Optional[int] = Query(default=None),
    end: Optional[int] = Query(default=None),
    limit: int = Query(default=350),
    # binance-style params (authoritative shape we support)
    symbol: Optional[str] = Query(default=None),
    interval: Optional[str] = Query(default=None),
    startTime: Optional[int] = Query(default=None),
    endTime: Optional[int] = Query(default=None),
):
    """
    HitBTC-specific /candles passthrough with binance-style query params:
      - Talks to HITBTC /api/3/public/candles (interval mapping applied)
      - Allowed params (authoritative): symbol, interval, limit, startTime, endTime (ms or sec)
      - If only limit provided, omit startTime/endTime entirely
      - Propagate non-2xx as errors; log upstream URL/status/body snippet
      - IMPORTANT: Always return an object {"candles":[...]} (even when upstream returns an array)
    """
    sym = (symbol or SYMBOL)
    ivl = (interval or "1m")
    lim = str(min(max(1, int(limit)), 1000))

    # Normalize and validate times
    now_ms = _now_ms()

    def _to_ms(v: Optional[int]) -> Optional[int]:
        if v is None:
            return None
        # accept seconds -> ms
        return v * 1000 if v <= 1_000_000_000_000 else v

    st_ms = _to_ms(startTime if startTime is not None else None)
    et_ms = _to_ms(endTime if endTime is not None else None)

    # Build HitBTC query (docs: /api/3/public/candles)
    q: Dict[str, str] = {
        "symbols": sym,
        "interval": _interval_to_hitbtc(ivl),
        "limit": lim,
    }

    # Only include times if BOTH present (HitBTC expects ISO or ms; we pass ms)
    if st_ms is not None and et_ms is not None:
        if et_ms > now_ms:
            et_ms = now_ms
        if st_ms >= et_ms:
            raise HTTPException(status_code=400, detail="startTime must be < endTime")
        q["from"] = str(st_ms)
        q["till"] = str(et_ms)

    params = urlparse.urlencode({k: v for k, v in q.items() if v not in (None, "")})
    upstream = f"{HITBTC_BASE_URL}/api/3/public/candles"
    url = f"{upstream}?{params}" if params else upstream

    # Call upstream; log URL/status/body snippet; propagate non-2xx
    try:
        req = urlreq.Request(url)
        with urlreq.urlopen(req, timeout=15) as resp:
            body = resp.read().decode("utf-8", "ignore")
            snippet = body[:200]
            log.info(f"[HITBTC] GET {url} -> {resp.status} body[:200]={snippet!r}")
            if resp.status < 200 or resp.status >= 300:
                raise HTTPException(status_code=resp.status, detail=snippet)
            try:
                data = json.loads(body)
            except Exception:
                raise HTTPException(status_code=502, detail="Invalid JSON from HitBTC")
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"hitbtc candles error: {e}")

    # --- Minimal change: ALWAYS wrap arrays with {"candles":[...]} ---
    # Bot expects an object with a "candles" array (binance-style schema marker).
    if isinstance(data, list):
        return {"candles": data}
    return data

@app.get("/accounts")
def accounts(limit: int = 250):
    # Public bridge: return empty list to keep parity; authenticated path omitted.
    return {"accounts": [], "has_next": False, "cursor": "", "size": 0}

@app.get("/balance/base")
def balance_base(product_id: str = Query(...)):
    base, _ = _split_product(product_id)
    accts = accounts()  # same process
    value = _sum_available(accts["accounts"], base)
    return {"asset": base, "available": value, "step": "0"}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(...)):
    _, quote = _split_product(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], quote)
    return {"asset": quote, "available": value, "step": "0"}

@app.get("/product/{product_id}")
def product_info(product_id: str = Path(...)):
    return price(product_id)

# --- Order endpoints placeholders (HitBTC key/secret flow not implemented in bridge) ---
@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    # Bridge intentionally does not place orders on HitBTC; keep interface stable.
    raise HTTPException(status_code=501, detail="orders not supported on hitbtc-bridge")

@app.post("/order/market")
def order_market(product_id: str = Query(...), side: str = Query(...), quote_size: str = Query(...)):
    raise HTTPException(status_code=501, detail="orders not supported on hitbtc-bridge")

@app.get("/order/{order_id}")
def order_get(order_id: str, product_id: str = Query(default=SYMBOL)):
    raise HTTPException(status_code=501, detail="orders not supported on hitbtc-bridge")

# ---- Runner ----
async def _runner():
    task = asyncio.create_task(_ws_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass
