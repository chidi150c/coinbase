#!/usr/bin/env python3 
# FILE: bridge_binance/ws_binance.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using Binance WS/REST.
import os, asyncio, json, time, hmac, hashlib, logging
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq, parse as urlparse
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path, Body
import websockets
import uvicorn

# ---- Logging ----
log = logging.getLogger("bridge-binance")
logging.basicConfig(level=logging.INFO)

# ---- Config (mirror coinbase .env knobs where applicable) ----
SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8789"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

BINANCE_API_KEY     = os.getenv("BINANCE_API_KEY", "").strip()
BINANCE_API_SECRET  = os.getenv("BINANCE_API_SECRET", "").strip()
BINANCE_BASE_URL    = os.getenv("BINANCE_BASE_URL", "https://api.binance.com").strip().rstrip("/")
BINANCE_RECV_WINDOW = os.getenv("BINANCE_RECV_WINDOW", "5000").strip()  # ms

# ---- In-memory state ----
last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}  # minuteStartMs -> [o,h,l,c,vol]
_first_tick_logged: bool = False  # TRACE: first-tick marker

# Prefer @bookTicker mid; fallback to @trade last
_last_mid_px: Optional[float] = None
_last_mid_ts: Optional[int] = None
_last_last_px: Optional[float] = None
_last_last_ts: Optional[int] = None

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
    # Map BTC-USD style to Binance spot convention BTCUSDT
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

def _binance_signed(path: str, params: Dict[str,str]) -> Dict:
    if not BINANCE_API_KEY or not BINANCE_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing BINANCE_API_KEY/SECRET")
    p = dict(params or {})
    p.setdefault("timestamp", str(_now_ms()))
    p.setdefault("recvWindow", BINANCE_RECV_WINDOW)
    q = urlparse.urlencode(p, doseq=True)
    sig = hmac.new(BINANCE_API_SECRET.encode("utf-8"), q.encode("utf-8"), hashlib.sha256).hexdigest()
    url = f"{BINANCE_BASE_URL}{path}?{q}&signature={sig}"
    req = urlreq.Request(url, method="GET", headers={"X-MBX-APIKEY": BINANCE_API_KEY})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON from Binance")

def _binance_signed_post(path: str, params: Dict[str,str]) -> Dict:
    if not BINANCE_API_KEY or not BINANCE_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing BINANCE_API_KEY/SECRET")
    p = dict(params or {})
    p.setdefault("timestamp", str(_now_ms()))
    p.setdefault("recvWindow", BINANCE_RECV_WINDOW)
    q = urlparse.urlencode(p, doseq=True)
    sig = hmac.new(BINANCE_API_SECRET.encode("utf-8"), q.encode("utf-8"), hashlib.sha256).hexdigest()
    url = f"{BINANCE_BASE_URL}{path}?{q}&signature={sig}"
    req = urlreq.Request(url, method="POST", headers={"X-MBX-APIKEY": BINANCE_API_KEY})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON from Binance")

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

# ---- WS consumer ----
async def _ws_loop():
    global last_price, last_ts_ms, _first_tick_logged, _last_mid_px, _last_mid_ts, _last_last_px, _last_last_ts
    sym = _normalize_symbol(SYMBOL).lower()
    url = f"wss://stream.binance.com:9443/stream?streams={sym}@bookTicker/{sym}@trade"
    log.info(f"[WS] connecting url={url}")
    backoff = 1
    attempt = 0
    while True:
        attempt += 1
        try:
            log.info(f"[WS] connect attempt={attempt}")
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
                log.info(f"[WS] connected streams={sym}@bookTicker,{sym}@trade")
                backoff = 1
                while True:
                    raw = await ws.recv()
                    ts = _now_ms()
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        continue
                    stream = msg.get("stream","")
                    data   = msg.get("data",{})
                    # Track feeds separately
                    if stream.endswith("@bookTicker"):
                        a = data.get("a"); b = data.get("b")
                        try:
                            if a is not None and b is not None:
                                _last_mid_px = (float(a)+float(b))/2.0
                                _last_mid_ts = ts
                        except Exception:
                            pass
                    elif stream.endswith("@trade"):
                        p = data.get("p") or data.get("price")
                        try:
                            if p is not None:
                                _last_last_px = float(p)
                                _last_last_ts = ts
                        except Exception:
                            pass

                    # Prefer mid updated within ~1s, else last, else keep prior (no stalling)
                    now_ms = _now_ms()
                    px = None
                    if _last_mid_px is not None and _last_mid_ts is not None and (now_ms - _last_mid_ts) <= 1000:
                        px = _last_mid_px
                        ts_for_px = _last_mid_ts
                    elif _last_last_px is not None:
                        px = _last_last_px
                        ts_for_px = _last_last_ts or ts
                    else:
                        px = last_price
                        ts_for_px = last_ts_ms or ts

                    if px and px > 0:
                        last_price = px
                        last_ts_ms = ts_for_px
                        _update_candle(px, ts_for_px)
                        if not _first_tick_logged:
                            _first_tick_logged = True
                            log.info(f"[WS] first tick px={px:.8f} ts_ms={ts_for_px}")
        except Exception as e:
            log.warning(f"[WS] disconnect err={e} backoff={backoff}s")
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

# Keep candles flowing even if both feeds stall briefly (emit last seen price)
async def _keepalive_loop():
    global last_price, last_ts_ms
    while True:
        await asyncio.sleep(1)
        if last_price is None:
            continue
        now_ms = _now_ms()
        if last_ts_ms is None or (now_ms - last_ts_ms) > 1000:
            last_ts_ms = now_ms
            _update_candle(last_price, now_ms)

# ---- FastAPI app ----
app = FastAPI(title="bridge-binance", version="0.3")

# Map Coinbase granularities → Binance intervals
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
# Reverse for alias mapping
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

@app.get("/candles")
def get_candles(
    # legacy names (ignored for Binance-specific handler)
    product_id: str = Query(default=SYMBOL),
    granularity: str = Query(default="ONE_MINUTE"),
    start: Optional[int] = Query(default=None),
    end: Optional[int] = Query(default=None),
    limit: int = Query(default=350),
    # binance-style params (authoritative)
    symbol: Optional[str] = Query(default=None),
    interval: Optional[str] = Query(default=None),
    startTime: Optional[int] = Query(default=None),
    endTime: Optional[int] = Query(default=None),
):
    """
    Binance-specific /candles passthrough:
      - Talks only to https://api.binance.com/api/v3/klines
      - Allowed params: symbol, interval, limit, startTime, endTime (ms)
      - startTime/endTime validation: convert seconds→ms, require start<end, clamp endTime to now
      - If only limit provided, omit startTime/endTime entirely
      - No cross-bridge aliasing or Coinbase product/granularity handling
      - Propagate non-2xx as errors; log upstream URL/status/body snippet
    """
    # Choose authoritative params strictly from Binance query names
    sym = (symbol or SYMBOL)
    ivl = (interval or "1m")
    lim = limit

    # Normalize and validate times
    now_ms = _now_ms()

    def _to_ms(v: Optional[int]) -> Optional[int]:
        if v is None:
            return None
        # accept seconds -> ms
        return v * 1000 if v <= 1_000_000_000_000 else v

    st_ms = _to_ms(startTime if startTime is not None else None)
    et_ms = _to_ms(endTime if endTime is not None else None)

    params = urlparse.urlencode(
        {k: v for k, v in {
            "symbol": sym,
            "interval": ivl,
            "limit": str(min(max(1, lim), 1000)),
        }.items() if v not in (None, "")}
    )

    # Only include times if BOTH present
    if st_ms is not None and et_ms is not None:
        if et_ms > now_ms:
            et_ms = now_ms
        if st_ms >= et_ms:
            raise HTTPException(status_code=400, detail="startTime must be < endTime")
        time_qs = urlparse.urlencode({"startTime": str(st_ms), "endTime": str(et_ms)})
        if params:
            params = f"{params}&{time_qs}"
        else:
            params = time_qs

    upstream = f"{BINANCE_BASE_URL}/api/v3/klines"
    url = f"{upstream}?{params}" if params else upstream

    # Call upstream; log URL/status/body snippet; propagate non-2xx
    try:
        req = urlreq.Request(url)
        with urlreq.urlopen(req, timeout=15) as resp:
            body = resp.read().decode("utf-8", "ignore")
            snippet = body[:200]
            log.info(f"[BINANCE] GET {url} -> {resp.status} body[:200]={snippet!r}")
            if resp.status < 200 or resp.status >= 300:
                raise HTTPException(status_code=resp.status, detail=snippet)
            try:
                data = json.loads(body)
            except Exception:
                raise HTTPException(status_code=502, detail="Invalid JSON from Binance")
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"binance klines error: {e}")

    # Return object-wrapped candles to match expected schema
    return {"candles": data}

@app.get("/accounts")
def accounts(limit: int = 250):
    payload = _binance_signed("/api/v3/account", {})
    bals = payload.get("balances") or []
    out = []
    for b in bals:
        asset = str(b.get("asset","")).upper()
        free  = str(b.get("free","0"))
        out.append({
            "currency": asset,
            "available_balance": {"value": free, "currency": asset},
            "type": "spot",
            "platform": "binance",
        })
    return {"accounts": out, "has_next": False, "cursor": "", "size": len(out)}

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

# --- Order endpoints (market by quote size), with partial-fill enrichment ---
def _my_trades(symbol: str, order_id: int) -> List[Dict]:
    return _binance_signed("/api/v3/myTrades", {"symbol": symbol, "orderId": str(order_id)})

@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    return order_market(product_id=product_id, side="BUY", quote_size=quote_size)

@app.post("/order/market")
def order_market(
    product_id: Optional[str] = Query(default=None),
    side: Optional[str] = Query(default=None),
    quote_size: Optional[str] = Query(default=None),
    body: Optional[Dict] = Body(default=None),
):
    # Optional JSON body fallback (no behavior change for existing clients using query params)
    if (product_id is None or side is None or quote_size is None) and isinstance(body, dict):
        product_id = product_id or body.get("product_id")
        side = (side or body.get("side"))
        qs = body.get("quote_size")
        quote_size = quote_size or (str(qs) if qs is not None else None)

    # Validate required fields after merging
    if not product_id or not side or not quote_size:
        raise HTTPException(status_code=422, detail="Missing required fields: product_id, side, quote_size (query or JSON body)")

    sym = _normalize_symbol(product_id)
    side = side.upper()
    payload = _binance_signed_post("/api/v3/order",
        {"symbol": sym, "side": side, "type": "MARKET", "quoteOrderQty": quote_size})

    order_id = payload.get("orderId")
    resp = {
        "order_id": str(order_id),
        "product_id": product_id,
        "status": "open",
        "created_at": _now_iso(),
        "filled_size": "0",
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": side.lower(),
    }

    try:
        trades = _my_trades(sym, int(order_id))
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("qty","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("commission","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T" if t.get("isBuyerMaker") else "M",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty * price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done"
    except Exception:
        pass
    return resp

@app.get("/order/{order_id}")
def order_get(order_id: str, product_id: str = Query(default=SYMBOL)):
    sym = _normalize_symbol(product_id)
    od = _binance_signed("/api/v3/order", {"symbol": sym, "orderId": order_id})
    resp = {
        "order_id": str(od.get("orderId")),
        "product_id": product_id,
        "status": "open" if od.get("status") in ("NEW","PARTIALLY_FILLED") else "done",
        "created_at": _now_iso(),
        "filled_size": str(od.get("executedQty","0")),
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": str(od.get("side","")).lower(),
    }
    try:
        trades = _my_trades(sym, int(order_id))
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("qty","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("commission","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T" if t.get("isBuyerMaker") else "M",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty * price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done" if str(od.get("status")) in ("FILLED","EXPIRED","CANCELED","REJECTED") else resp["status"]
    except Exception:
        pass
    return resp

# ---- Runner ----
async def _runner():
    task_ws = asyncio.create_task(_ws_loop())
    task_keepalive = asyncio.create_task(_keepalive_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task_ws, task_keepalive, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass
