#!/usr/bin/env python3 
# FILE: bridge_binance/ws_binance.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using Binance WS/REST.
import os, asyncio, json, time, hmac, hashlib, logging
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq, parse as urlparse
from urllib.error import HTTPError
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

# Optional local step fallbacks
BASE_STEP_FALLBACK  = os.getenv("BASE_STEP", "").strip()
QUOTE_STEP_FALLBACK = os.getenv("QUOTE_STEP", "").strip()

# ---- In-memory state ----
last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}  # minuteStartMs -> [o,h,l,c,vol]
_first_tick_logged: bool = False  # TRACE: first-tick marker

# Simple cache for symbol metadata (LOT_SIZE.stepSize, quotePrecision → quote step)
_symbol_meta_cache: Dict[str, Dict[str, str]] = {}

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

def _hint_for_binance_error(code, msg):
    try:
        c = int(code) if code is not None else None
    except Exception:
        c = None
    if c == -1013:
        return "Order notional or quantity violates symbol filters (MIN_NOTIONAL/LOT_SIZE); increase size or round qty."
    if c == -2010:
        return "Insufficient balance; reduce order size or add funds."
    if c == -1021:
        return "Timestamp outside recvWindow; sync system clock or increase BINANCE_RECV_WINDOW."
    return "See binance_code/binance_msg for details."

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
    try:
        with urlreq.urlopen(req, timeout=10) as resp:
            body = resp.read().decode("utf-8")
            if resp.status != 200:
                raise HTTPException(status_code=resp.status, detail=body[:200])
            try:
                return json.loads(body)
            except Exception:
                raise HTTPException(status_code=500, detail="Invalid JSON from Binance")
    except HTTPError as e:
        # Attempt to parse Binance JSON error and surface as a 400 with structured detail
        raw = ""
        try:
            raw = e.read().decode("utf-8", "ignore")
            payload = json.loads(raw) if raw else {}
        except Exception:
            payload = {}
        detail = {
            "exchange": "binance",
            "endpoint": path,
            "http_status": e.code,
            "binance_code": payload.get("code"),
            "binance_msg": payload.get("msg"),
            "hint": _hint_for_binance_error(payload.get("code"), payload.get("msg")),
        }
        raise HTTPException(status_code=400, detail=detail)

def _binance_signed_delete(path: str, params: Dict[str,str]) -> Dict:
    if not BINANCE_API_KEY or not BINANCE_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing BINANCE_API_KEY/SECRET")
    p = dict(params or {})
    p.setdefault("timestamp", str(_now_ms()))
    p.setdefault("recvWindow", BINANCE_RECV_WINDOW)
    q = urlparse.urlencode(p, doseq=True)
    sig = hmac.new(BINANCE_API_SECRET.encode("utf-8"), q.encode("utf-8"), hashlib.sha256).hexdigest()
    url = f"{BINANCE_BASE_URL}{path}?{q}&signature={sig}"
    req = urlreq.Request(url, method="DELETE", headers={"X-MBX-APIKEY": BINANCE_API_KEY})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body) if body else {}
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

def _get_symbol_steps(sym: str) -> Tuple[str, str]:
    """
    Return (base_step, quote_step) as strings for given Binance symbol.
    base_step  = LOT_SIZE.stepSize
    quote_step = 10^(-quotePrecision)  # for quoteOrderQty increments
    """
    key = sym.upper()
    if key in _symbol_meta_cache:
        meta = _symbol_meta_cache[key]
        return meta.get("base_step", "0"), meta.get("quote_step", "0")

    url = f"{BINANCE_BASE_URL}/api/v3/exchangeInfo?symbol={key}"
    info = _http_get(url)
    base_step = "0"
    quote_step = "0"
    try:
        symbols = info.get("symbols") or []
        if symbols:
            s = symbols[0]
            # LOT_SIZE -> base step
            for f in (s.get("filters") or []):
                if f.get("filterType") == "LOT_SIZE":
                    step = str(f.get("stepSize", "0"))
                    if step and step != "0":
                        base_step = step
                        break
            # quotePrecision -> quote step (as decimal increment for quoteOrderQty)
            qp = s.get("quotePrecision")
            if isinstance(qp, int) and qp >= 0:
                quote_step = f"{1/(10**qp):.{qp}f}" if qp > 0 else "1"
    except Exception:
        pass

    _symbol_meta_cache[key] = {"base_step": base_step, "quote_step": quote_step}
    return base_step, quote_step

# ---- WS consumer ----
async def _ws_loop():
    """
    Prefer last trade (@trade) as the primary tick.
    Fallback to orderbook mid (@bookTicker) if trade stalls (> TRADE_STALL_MS).
    Never block candle updates; always use the freshest available source.
    """
    global last_price, last_ts_ms, _first_tick_logged
    sym = _normalize_symbol(SYMBOL).lower()
    url = f"wss://stream.binance.com:9443/stream?streams={sym}@bookTicker/{sym}@trade"
    log.info(f"[WS] connecting url={url}")
    backoff = 1
    attempt = 0

    # Minimal local stall threshold (ms) for trade before falling back to mid
    TRADE_STALL_MS = 1000

    # Track both feeds separately
    last_trade_px: Optional[float] = None
    last_trade_ts: Optional[int] = None
    last_mid_px: Optional[float] = None
    last_mid_ts: Optional[int] = None

    while True:
        attempt += 1
        try:
            log.info(f"[WS] connect attempt={attempt}")
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
                log.info(f"[WS] connected streams={sym}@bookTicker,{sym}@trade")
                backoff = 1
                while True:
                    raw = await ws.recv()
                    now_ms = _now_ms()
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        continue
                    stream = msg.get("stream","")
                    data   = msg.get("data",{})

                    # Update per-source caches
                    if stream.endswith("@trade"):
                        p = data.get("p") or data.get("price")
                        try:
                            if p is not None:
                                last_trade_px = float(p)
                                last_trade_ts = now_ms
                        except Exception:
                            pass
                    elif stream.endswith("@bookTicker"):
                        a = data.get("a"); b = data.get("b")
                        try:
                            if a is not None and b is not None:
                                mid = (float(a) + float(b)) / 2.0
                                if mid > 0:
                                    last_mid_px = mid
                                    last_mid_ts = now_ms
                        except Exception:
                            pass

                    # Choose preferred tick: trade if fresh, else mid if available
                    chosen_px = None
                    if last_trade_px and last_trade_ts:
                        if now_ms - last_trade_ts <= TRADE_STALL_MS:
                            chosen_px = last_trade_px
                    if chosen_px is None and last_mid_px:
                        chosen_px = last_mid_px

                    if chosen_px and chosen_px > 0:
                        last_price = chosen_px
                        last_ts_ms = now_ms
                        _update_candle(chosen_px, now_ms)
                        if not _first_tick_logged:
                            _first_tick_logged = True
                            log.info(f"[WS] first tick px={chosen_px:.8f} ts_ms={now_ms}")
        except Exception as e:
            log.warning(f"[WS] disconnect err={e} backoff={backoff}s")
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

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
    # legacy (Coinbase-like) params
    product_id: Optional[str] = Query(default=None),
    granularity: Optional[str] = Query(default=None),
    start: Optional[int] = Query(default=None),  # seconds
    end: Optional[int] = Query(default=None),    # seconds
    limit: int = Query(default=350),
    # Binance-native params
    symbol: Optional[str] = Query(default=None),
    interval: Optional[str] = Query(default=None),
    startTime: Optional[int] = Query(default=None),  # ms or seconds
    endTime: Optional[int] = Query(default=None),    # ms or seconds
):
    """
    /candles dual-mode:
      • Legacy mode (product_id + granularity [+ start/end seconds]): returns Coinbase-normalized objects.
      • Binance mode (symbol + interval [+ startTime/endTime ms]): passthrough array-of-arrays (wrapped).
    """
    legacy_mode = (symbol is None and interval is None)

    # Resolve symbol/interval either from native params or legacy aliases
    if legacy_mode:
        pid = (product_id or SYMBOL)
        sym = _normalize_symbol(pid)
        ivl = GRAN_MAP.get((granularity or "ONE_MINUTE").upper(), "1m")
        st_s = start
        et_s = end
    else:
        sym = (symbol or SYMBOL)
        ivl = (interval or "1m")
        st_s = None
        et_s = None

    # Normalize and validate times
    now_ms = _now_ms()

    def _to_ms(v: Optional[int]) -> Optional[int]:
        if v is None:
            return None
        # accept seconds -> ms
        return v * 1000 if v <= 1_000_000_000_000 else v

    # Prefer legacy start/end when in legacy mode; else take native startTime/endTime
    st_ms = _to_ms(st_s if legacy_mode else (startTime if startTime is not None else None))
    et_ms = _to_ms(et_s if legacy_mode else (endTime if endTime is not None else None))

    # ✅ define lim before using it
    lim = max(1, min(int(limit), 1000))

    params = urlparse.urlencode(
        {k: v for k, v in {
            "symbol": sym,
            "interval": ivl,
            "limit": str(lim),
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

    # Shape:
    #  - legacy mode => normalize array-of-arrays into [{"start","open","high","low","close","volume"}...]
    #  - native mode => passthrough array-of-arrays (wrapped)
    if not isinstance(data, list):
        raise HTTPException(status_code=502, detail="Unexpected klines shape")
    if not legacy_mode:
        return {"candles": data}

    out = []
    for row in data:
        # Binance kline: [ openTime(ms), open, high, low, close, volume, closeTime, ... ]
        if not isinstance(row, list) or len(row) < 6:
            continue
        open_ms, o, h, l, c, v = row[0], row[1], row[2], row[3], row[4], row[5]
        try:
            start_sec = int(int(float(open_ms)) // 1000)
            out.append({
                "start":  str(start_sec),
                "open":   str(o),
                "high":   str(h),
                "low":    str(l),
                "close":  str(c),
                "volume": str(v),
            })
        except Exception:
            continue
    return {"candles": out}

@app.get("/accounts")
def accounts(limit: int = 250):
    payload = _binance_signed("/api/v3/account", {})
    bals = payload.get("balances") or []
    out = []
    for b in bals:
        asset  = str(b.get("asset","")).upper()
        free   = str(b.get("free","0"))
        locked = str(b.get("locked","0"))  # <-- include optional locked (hold) amount
        out.append({
            "currency": asset,
            "available_balance": {"value": free, "currency": asset},
            "locked_balance":    {"value": locked, "currency": asset},  # <-- new optional field
            "type": "spot",
            "platform": "binance",
        })
    return {"accounts": out, "has_next": False, "cursor": "", "size": len(out)}

@app.get("/balance/base")
def balance_base(product_id: str = Query(...)):
    base, _ = _split_product(product_id)
    accts = accounts()  # same process
    value = _sum_available(accts["accounts"], base)

    # Derive base step from exchange info (LOT_SIZE.stepSize) with env fallback
    sym = _normalize_symbol(product_id)
    base_step, _quote_step = _get_symbol_steps(sym)
    step = BASE_STEP_FALLBACK or base_step or "0"

    return {"asset": base, "available": value, "step": step}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(...)):
    _, quote = _split_product(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], quote)

    # Derive quote step from exchange info (quotePrecision) with env fallback
    sym = _normalize_symbol(product_id)
    _base_step, quote_step = _get_symbol_steps(sym)
    step = QUOTE_STEP_FALLBACK or quote_step or "0"

    return {"asset": quote, "available": value, "step": step}

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

# --- NEW: Post-only limit (LIMIT_MAKER). Body or query accepted. Returns {order_id} ---
@app.post("/order/limit_post_only")
def order_limit_post_only(
    product_id: Optional[str] = Query(default=None),
    side: Optional[str] = Query(default=None),
    limit_price: Optional[str] = Query(default=None),
    base_size: Optional[str] = Query(default=None),
    body: Optional[Dict] = Body(default=None),
):
    # Merge JSON body fallback if provided
    if (product_id is None or side is None or limit_price is None or base_size is None) and isinstance(body, dict):
        product_id = product_id or body.get("product_id")
        side        = (side or body.get("side"))
        lp          = body.get("limit_price")
        bs          = body.get("base_size")
        limit_price = limit_price or (str(lp) if lp is not None else None)
        base_size   = base_size   or (str(bs) if bs is not None else None)

    # Validate
    if not product_id or not side or not limit_price or not base_size:
        raise HTTPException(status_code=422, detail="Missing required fields: product_id, side, limit_price, base_size (query or JSON body)")

    sym = _normalize_symbol(product_id)
    side = side.upper()
    # LIMIT_MAKER is Binance's post-only order. It is rejected if it would trade immediately.
    payload = _binance_signed_post(
        "/api/v3/order",
        {
            "symbol": sym,
            "side": side,
            "type": "LIMIT_MAKER",
            "price": str(limit_price),
            "quantity": str(base_size),
            # timeInForce not required for LIMIT_MAKER
        },
    )
    order_id = payload.get("orderId")
    if not order_id:
        # surface upstream details if absent
        raise HTTPException(status_code=502, detail=f"Binance did not return orderId: {json.dumps(payload)[:200]}")
    return {"order_id": str(order_id)}

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
            resp["status"] = "done" if str(od.get("status")) in ("FILLED","EXPIRED","CANCELED","REJECTED") else "open" if resp["status"] == "open" else resp["status"]
    except Exception:
        pass
    return resp

# --- NEW: Cancel order endpoint (DELETE) to align with broker.CancelOrder ---
@app.delete("/order/{order_id}")
def order_cancel(order_id: str, product_id: str = Query(default=SYMBOL)):
    sym = _normalize_symbol(product_id)
    _ = _binance_signed_delete("/api/v3/order", {"symbol": sym, "orderId": order_id})
    return {"ok": True, "order_id": str(order_id)}

# ---- Runner ----
async def _runner():
    task = asyncio.create_task(_ws_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass
