#!/usr/bin/env python3
# FILE: bridge_hitbtc/ws_hitbtc.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using HitBTC WS/REST.
import os, asyncio, json, time, base64, logging, traceback
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq, parse as urlparse
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path, Request, Body
import websockets
import uvicorn

# ---- Logging ----
log = logging.getLogger("bridge-hitbtc")
LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
logging.basicConfig(level=getattr(logging, LOG_LEVEL, logging.INFO),
                    format="%(asctime)s %(levelname)s %(name)s: %(message)s")

SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8788"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

# Heartbeat behavior: if true, heartbeat will also touch last_ts_ms (keeps stale=false).
HEARTBEAT_TOUCH_TS = os.getenv("HEARTBEAT_TOUCH_TS", "false").lower() == "true"
# Heartbeat interval seconds
HEARTBEAT_SEC = float(os.getenv("HEARTBEAT_SEC", "1"))

HITBTC_API_KEY    = os.getenv("HITBTC_API_KEY", "").strip()
HITBTC_API_SECRET = os.getenv("HITBTC_API_SECRET", "").strip()
HITBTC_BASE_URL   = os.getenv("HITBTC_BASE_URL", "https://api.hitbtc.com").strip().rstrip("/")

last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}  # minute_ms -> [o,h,l,c,vol]
_first_tick_logged: bool = False  # TRACE: first-tick marker

# Internal WS preference trackers (orderbook mid primary, ticker last fallback)
_last_mid_px: Optional[float] = None
_last_mid_ts: Optional[int] = None
_last_last_px: Optional[float] = None
_last_last_ts: Optional[int] = None

# ---- Time & candle helpers ----
def _now_ms() -> int:
    return int(time.time() * 1000)

def _now_iso() -> str:
    return datetime.utcnow().replace(tzinfo=timezone.utc).isoformat()

def _minute_start(ts_ms: int) -> int:
    return (ts_ms // 60000) * 60000

def _trim_old(max_minutes=6000):
    if len(candles) <= max_minutes:
        return
    drop = len(candles) - max_minutes
    for k in sorted(candles.keys())[:-max_minutes]:
        candles.pop(k, None)
    log.debug(f"[CANDLES] trimmed old entries: dropped={drop}, kept={len(candles)}")

def _update_candle(px: float, ts_ms: int, vol: float = 0.0):
    m = _minute_start(ts_ms)
    if m not in candles:
        candles[m] = [px, px, px, px, vol]
        log.debug(f"[CANDLES] new minute bucket m={m//1000} o=h=l=c={px} v={vol}")
    else:
        o, h, l, c, v = candles[m]
        nh = px if px > h else h
        nl = px if px < l else l
        candles[m] = [o, nh, nl, px, v + vol]
        log.debug(f"[CANDLES] update m={m//1000} o={o} h={nh} l={nl} c={px} v={v+vol}")
    _trim_old()

def _normalize_symbol(pid: str) -> str:
    return (pid or SYMBOL).upper().replace("-", "")

def _http_get(url: str, headers: Optional[Dict[str, str]] = None):
    log.debug(f"[REST] GET {url}")
    req = urlreq.Request(url, headers=headers or {})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8", "ignore")
        if resp.status != 200:
            log.warning(f"[REST] non-200 {resp.status} for {url} body={body[:200]}")
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            j = json.loads(body)
            log.debug(f"[REST] OK {url} len={len(body)}")
            return j
        except Exception:
            log.error(f"[REST] invalid JSON for {url}")
            raise HTTPException(status_code=500, detail="Invalid JSON")

def _req(path: str, method="GET", body: Optional[bytes] = None) -> Dict:
    if not HITBTC_API_KEY or not HITBTC_API_SECRET:
        log.error("[AUTH] Missing HITBTC_API_KEY/SECRET")
        raise HTTPException(status_code=401, detail="Missing HITBTC_API_KEY/SECRET")
    url = f"{HITBTC_BASE_URL}{path}"
    token = base64.b64encode(f"{HITBTC_API_KEY}:{HITBTC_API_SECRET}".encode("utf-8")).decode("ascii")
    headers = {"Authorization": f"Basic {token}"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    log.debug(f"[REST AUTH] {method} {url} body_len={0 if body is None else len(body)}")
    req = urlreq.Request(url, method=method, data=body, headers=headers)
    with urlreq.urlopen(req, timeout=10) as resp:
        data = resp.read().decode("utf-8")
        if resp.status != 200:
            log.warning(f"[REST AUTH] non-200 {resp.status} for {url} body={data[:200]}")
            raise HTTPException(status_code=resp.status, detail=data[:200])
        try:
            j = json.loads(data)
            log.debug(f"[REST AUTH] OK {url}")
            return j
        except Exception:
            log.error(f"[REST AUTH] invalid JSON for {url}")
            raise HTTPException(status_code=500, detail="Invalid JSON from HitBTC")

def _split(pid: str) -> Tuple[str, str]:
    p = pid.upper().replace("-", "")
    if len(p) > 3:
        return p[:-3], p[-3:]
    return p, "USD"

def _sum_available(accts: List[Dict], asset: str) -> str:
    a = asset.upper()
    for r in accts:
        if r.get("currency", "").upper() == a:
            return str(r.get("available_balance", {}).get("value", "0"))
    return "0"

# --- symbol metadata cache for steps (quantity_increment, tick_size) ---
_symbol_meta_cache: Dict[str, Dict[str, str]] = {}

def _get_symbol_meta(sym: str) -> Dict[str, str]:
    """Fetch and cache HitBTC symbol metadata; returns {} on failure."""
    key = sym.upper()
    if key in _symbol_meta_cache:
        return _symbol_meta_cache[key]
    try:
        url = f"{HITBTC_BASE_URL}/api/3/public/symbol/{key}"
        j = _http_get(url)
        if isinstance(j, dict) and key in j and isinstance(j[key], dict):
            meta = j[key]
        elif isinstance(j, dict):
            meta = j
        else:
            meta = {}
        out = {
            "quantity_increment": str(meta.get("quantity_increment", "") or ""),
            "tick_size": str(meta.get("tick_size", "") or ""),
        }
        _symbol_meta_cache[key] = out
        log.debug(f"[META] cached {key} quantity_increment={out['quantity_increment']} tick_size={out['tick_size']}")
        return out
    except Exception as e:
        log.debug(f"[META] fetch failed for {key}: {e}")
        _symbol_meta_cache[key] = {}
        return {}

def _env_step(name: str) -> Optional[str]:
    """Return env override if set to positive numeric string, else None."""
    v = os.getenv(name, "").strip()
    if not v:
        return None
    try:
        if float(v) > 0:
            return v
    except Exception:
        pass
    return None

# Map Coinbase granularities → HitBTC periods
GRAN_MAP = {
    "ONE_MINUTE": "M1",
    "FIVE_MINUTE": "M5",
    "FIFTEEN_MINUTE": "M15",
    "THIRTY_MINUTE": "M30",
    "ONE_HOUR": "H1",
    "FOUR_HOUR": "H4",
    "ONE_DAY": "D1",
    # Approximate for unsupported:
    "TWO_HOUR": "H1",
    "SIX_HOUR": "H4",
}
_PERIOD_TO_GRAN = {v: k for k, v in GRAN_MAP.items()}

# ---- WebSocket: HitBTC market data (real-tick updates)
async def _ws_loop():
    """
    HitBTC public WS:
      - subscribe to `orderbook/top/1000ms` (primary: mid = (bid+ask)/2)
      - subscribe to `ticker/price/1s`      (fallback: last price 'c')
    Updates happen only on real WS ticks.
    """
    global last_price, last_ts_ms, _first_tick_logged, _last_mid_px, _last_mid_ts, _last_last_px, _last_last_ts
    url = "wss://api.hitbtc.com/api/3/ws/public"
    sym = _normalize_symbol(SYMBOL)
    subs = [
        {"method": "subscribe", "ch": "orderbook/top/1000ms", "params": {"symbols": [sym]}, "id": 1},
        {"method": "subscribe", "ch": "ticker/price/1s",      "params": {"symbols": [sym]}, "id": 2},
    ]
    backoff = 1
    conn_attempt = 0
    while True:
        conn_attempt += 1
        try:
            log.info(f"[WS] connecting url={url} attempt={conn_attempt}")
            async with websockets.connect(
                url, ping_interval=25, ping_timeout=25, max_queue=1024
            ) as ws:
                for s in subs:
                    await ws.send(json.dumps(s))
                log.info(f"[WS] subscribed streams for {sym}: orderbook/top/1000ms, ticker/price/1s")
                backoff = 1

                while True:
                    raw = await ws.recv()
                    if LOG_LEVEL == "DEBUG":
                        log.debug(f"[WS] recv: {raw[:240]}{'...' if len(raw)>240 else ''}")
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        log.debug("[WS] non-json message ignored")
                        continue
                    if not isinstance(msg, dict):
                        continue

                    if "result" in msg and msg.get("id") in (1, 2):
                        log.debug(f"[WS] subscribe ack id={msg.get('id')} result={msg.get('result')}")
                        continue

                    ch = msg.get("ch")
                    data = msg.get("data")
                    if ch not in ("orderbook/top/1000ms", "ticker/price/1s") or not data:
                        continue

                    row = data.get(sym) or {}

                    # Normalize WS timestamp (seconds → ms if needed)
                    t_raw = row.get("t")
                    try:
                        ts_ms = int(t_raw) if t_raw is not None else _now_ms()
                        if ts_ms < 1_000_000_000_000:
                            ts_ms *= 1000
                    except Exception:
                        ts_ms = _now_ms()

                    # Track feeds
                    if ch == "orderbook/top/1000ms":
                        try:
                            a = float(row.get("a") or 0.0)
                            b = float(row.get("b") or 0.0)
                            if a > 0 and b > 0:
                                _last_mid_px = (a + b) / 2.0
                                _last_mid_ts = ts_ms
                        except Exception:
                            pass
                    elif ch == "ticker/price/1s":
                        c = row.get("c")
                        try:
                            if c is not None:
                                _last_last_px = float(c)
                                _last_last_ts = ts_ms
                        except Exception:
                            pass

                    # Choose price: prefer mid updated within ~1s, else last
                    now_ms = _now_ms()
                    px = None
                    ts_for_px = None
                    if _last_mid_px is not None and _last_mid_ts is not None and (now_ms - _last_mid_ts) <= 1000:
                        px = _last_mid_px
                        ts_for_px = _last_mid_ts
                    elif _last_last_px is not None and _last_last_ts is not None:
                        px = _last_last_px
                        ts_for_px = _last_last_ts

                    # Update on real tick
                    if px is not None and px > 0 and ts_for_px is not None:
                        last_price = px
                        # Use receipt time for staleness
                        last_ts_ms = now_ms
                        _update_candle(px, ts_for_px, 0.0)
                        if not _first_tick_logged:
                            _first_tick_logged = True
                            log.info(f"[WS] first tick sym={sym} px={px:.8f} ts_ms={ts_for_px}")
        except Exception as e:
            log.warning(f"[WS] disconnect err={e} backoff={backoff}s")
            if LOG_LEVEL == "DEBUG":
                log.debug(traceback.format_exc())
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 15)

# ---- Heartbeat: keep candles continuous; staleness touch is configurable
async def _keepalive_loop():
    global last_price, last_ts_ms
    while True:
        await asyncio.sleep(HEARTBEAT_SEC)
        if last_price is None:
            continue
        now_ms = _now_ms()
        # Always keep candles continuous
        _update_candle(last_price, now_ms, 0.0)
        # Optionally keep /price fresh (staleness off)
        if HEARTBEAT_TOUCH_TS:
            last_ts_ms = now_ms

# ---- HTTP app
app = FastAPI(title="bridge-hitbtc", version="0.8")

@app.middleware("http")
async def _access_log(request: Request, call_next):
    start = time.time()
    try:
        response = await call_next(request)
    finally:
        duration_ms = int((time.time() - start) * 1000)
        log.info(f"[HTTP] {request.method} {request.url.path} q={dict(request.query_params)} {duration_ms}ms")
    return response

@app.get("/health")
def health():
    age_ms = None
    stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms() - last_ts_ms)
        stale = age_ms > STALE_MS
    return {
        "ok": True,
        "exchange": "hitbtc",
        "symbol": SYMBOL,
        "price": float(last_price) if last_price else 0.0,
        "age_ms": age_ms,
        "stale": stale,
    }

@app.get("/price")
def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None
    stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms() - last_ts_ms)
        stale = age_ms > STALE_MS
    log.debug(f"[PRICE] product_id={product_id} price={last_price} ts_ms={last_ts_ms} stale={stale}")
    return {
        "product_id": product_id,
        "price": float(last_price) if last_price else 0.0,
        "ts": last_ts_ms,
        "stale": stale,
    }

# ---- /candles (dual-mode like Binance bridge) ----
@app.get("/candles")
def get_candles(
    # Legacy (Coinbase-like) params
    product_id: Optional[str] = Query(default=None),
    granularity: Optional[str] = Query(default=None),
    start: Optional[int] = Query(default=None),  # seconds
    end: Optional[int] = Query(default=None),    # seconds
    limit: int = Query(default=350),
    # Native params (preferred)
    symbol: Optional[str] = Query(default=None),
    interval: Optional[str] = Query(default=None),
    startTime: Optional[int] = Query(default=None),  # ms or seconds
    endTime: Optional[int] = Query(default=None),    # ms or seconds
):
    legacy_mode = (symbol is None and interval is None)

    # Resolve symbol & period
    if legacy_mode:
        pid = (product_id or SYMBOL)
        sym = _normalize_symbol(pid)
        gran = (granularity or "ONE_MINUTE").upper()
        period = GRAN_MAP.get(gran, "M1")
        st_s = start
        et_s = end
    else:
        sym = _normalize_symbol(symbol or SYMBOL)
        ivl = (interval or "1m").strip().lower()
        period_map = {"1m": "M1", "5m": "M5", "15m": "M15", "30m": "M30", "1h": "H1", "2h": "H1", "4h": "H4", "6h": "H4", "1d": "D1"}
        period = period_map.get(ivl)
        if not period:
            raise HTTPException(status_code=400, detail=f"unsupported interval {interval}")
        st_s = None
        et_s = None

    now_ms = _now_ms()
    def _to_ms(v: Optional[int]) -> Optional[int]:
        if v is None: return None
        return v * 1000 if v <= 1_000_000_000_000 else v

    st_ms = _to_ms(st_s if legacy_mode else (startTime if startTime is not None else None))
    et_ms = _to_ms(et_s if legacy_mode else (endTime if endTime is not None else None))

    params = {
        "period": period,
        "limit": str(min(max(1, limit), 1000)),
    }
    if st_ms is not None and et_ms is not None:
        if et_ms > now_ms:
            et_ms = now_ms
        if st_ms >= et_ms:
            raise HTTPException(status_code=400, detail="startTime must be < endTime")
        def _rfc3339(ms: int) -> str:
            return datetime.utcfromtimestamp(ms / 1000).replace(tzinfo=timezone.utc).isoformat().replace("+00:00", "Z")
        params["from"] = _rfc3339(st_ms)
        params["till"] = _rfc3339(et_ms)

    qs = urlparse.urlencode(params)
    upstream = f"{HITBTC_BASE_URL}/api/3/public/candles/{sym}"
    url = f"{upstream}?{qs}" if qs else upstream

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

    rows = data if isinstance(data, list) else data.get("candles", [])
    if not isinstance(rows, list):
        raise HTTPException(status_code=502, detail="Unexpected candles shape")

    def _pick(d: dict, *keys, default="0"):
        for k in keys:
            if k in d and d[k] is not None:
                return d[k]
        return default

    if legacy_mode:
        out = []
        for r in rows:
            if not isinstance(r, dict): continue
            ts = _pick(r, "timestamp", "t")
            try:
                if isinstance(ts, (int, float)):
                    start_sec = int(float(ts)) // 1000 if float(ts) > 1e12 else int(float(ts))
                else:
                    start_sec = int(datetime.fromisoformat(str(ts).replace("Z","+00:00")).timestamp())
            except Exception:
                continue
            o = str(_pick(r, "open", "o", default="0"))
            h = str(_pick(r, "max", "high", "h", default="0"))
            l = str(_pick(r, "min", "low", "l", default="0"))
            c = str(_pick(r, "close", "c", default="0"))
            v = str(_pick(r, "volume", "v", default="0"))
            out.append({
                "start":  str(start_sec),
                "open":   o,
                "high":   h,
                "low":    l,
                "close":  c,
                "volume": v,
            })
        return {"candles": out}
    else:
        klines = []
        for r in rows:
            if not isinstance(r, dict): continue
            ts = _pick(r, "timestamp", "t")
            try:
                if isinstance(ts, (int, float)):
                    open_ms = int(float(ts))
                    if open_ms < 1e12:
                        open_ms *= 1000
                else:
                    open_ms = int(datetime.fromisoformat(str(ts).replace("Z","+00:00")).timestamp() * 1000)
            except Exception:
                continue
            o = float(_pick(r, "open", "o", default="0"))
            h = float(_pick(r, "max", "high", "h", default="0"))
            l = float(_pick(r, "min", "low", "l", default="0"))
            c = float(_pick(r, "close", "c", default="0"))
            v = float(_pick(r, "volume", "v", default="0"))
            klines.append([open_ms, str(o), str(h), str(l), str(c), str(v), open_ms + 1])
        return {"candles": klines}

@app.get("/accounts")
def accounts(limit: int = 250):
    payload = _req("/api/3/spot/balance")
    rows = payload if isinstance(payload, list) else payload.get("balance", [])
    out = []
    for r in rows:
        cur = str(r.get("currency", "")).upper()
        avail = str(r.get("available", r.get("cash", "0")))
        out.append({
            "currency": cur,
            "available_balance": {"value": avail, "currency": cur},
            "type": "spot",
            "platform": "hitbtc",
        })
    log.info(f"[ACCOUNTS] size={len(out)}")
    return {"accounts": out, "has_next": False, "cursor": "", "size": len(out)}

@app.get("/balance/base")
def balance_base(product_id: str = Query(...)):
    base, _ = _split(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], base)
    step = _env_step("BASE_STEP")
    if step is None:
        meta = _get_symbol_meta(_normalize_symbol(product_id))
        qinc = meta.get("quantity_increment") or ""
        try:
            if qinc and float(qinc) > 0:
                step = qinc
        except Exception:
            step = None
    if step is None:
        step = "0"
    log.info(f"[BALANCE] base asset={base} available={value}")
    return {"asset": base, "available": value, "step": step}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(...)):
    _, quote = _split(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], quote)
    step = _env_step("QUOTE_STEP")
    if step is None:
        meta = _get_symbol_meta(_normalize_symbol(product_id))
        tick = meta.get("tick_size") or ""
        try:
            if tick and float(tick) > 0:
                step = tick
        except Exception:
            step = None
    if step is None:
        step = "0"
    log.info(f"[BALANCE] quote asset={quote} available={value}")
    return {"asset": quote, "available": value, "step": step}

@app.get("/product/{product_id}")
def product_info(product_id: str = Path(...)):
    resp = price(product_id)
    log.debug(f"[PRODUCT] {product_id} -> price={resp.get('price')} ts={resp.get('ts')}")
    return resp

# --- Orders (market), partial-fill enrichment ---
def _place_order(symbol: str, side: str, quantity: str) -> Dict:
    body = json.dumps({
        "symbol": symbol, "side": side.lower(), "type": "market", "quantity": quantity
    }).encode("utf-8")
    return _req("/api/3/spot/order", method="POST", body=body)

def _get_order(order_id: str) -> Dict:
    return _req(f"/api/3/spot/order/{order_id}")

def _order_trades(order_id: str) -> List[Dict]:
    payload = _req(f"/api/3/spot/order/{order_id}/trades")
    return payload if isinstance(payload, list) else payload.get("trades", [])

# --- NEW: Limit Post-Only helpers & endpoints ---

def _place_limit_post_only(symbol: str, side: str, price: str, quantity: str) -> Dict:
    """
    HitBTC post-only limit:
      POST /api/3/spot/order
      { "symbol", "side": "buy"/"sell", "type": "limit", "timeInForce": "PO", "postOnly": true, "price", "quantity" }
    """
    body = json.dumps({
        "symbol": symbol,
        "side": side.lower(),
        "type": "limit",
        "timeInForce": "PO",
        "postOnly": True,  # some SDKs accept this, server ignores if timeInForce=PO is honored
        "price": price,
        "quantity": quantity,
    }).encode("utf-8")
    return _req("/api/3/spot/order", method="POST", body=body)

def _cancel_order(order_id: str) -> Dict:
    # HitBTC: DELETE /api/3/spot/order/{id}
    return _req(f"/api/3/spot/order/{order_id}", method="DELETE", body=None)

@app.post("/order/limit_post_only")
def order_limit_post_only(
    product_id: Optional[str] = Query(default=None),
    side: Optional[str] = Query(default=None),
    limit_price: Optional[str] = Query(default=None),
    base_size: Optional[str] = Query(default=None),
    body: Optional[Dict] = Body(default=None),
):
    """
    Places a post-only limit order. Returns just {"order_id": "..."}; the bot polls /order/{id}.
    Accepts query params or JSON body with the same keys.
    """
    if (product_id is None or side is None or limit_price is None or base_size is None) and isinstance(body, dict):
        product_id  = product_id  or body.get("product_id")
        side        = side        or body.get("side")
        limit_price = limit_price or (str(body.get("limit_price")) if body.get("limit_price") is not None else None)
        base_size   = base_size   or (str(body.get("base_size")) if body.get("base_size") is not None else None)

    if not product_id or not side or not limit_price or not base_size:
        raise HTTPException(status_code=422, detail="Missing required fields: product_id, side, limit_price, base_size (query or JSON)")

    sym = _normalize_symbol(product_id)
    side = side.upper()
    # basic sanity
    try:
        if Decimal(limit_price) <= 0 or Decimal(base_size) <= 0:
            raise Exception("non-positive")
    except Exception:
        raise HTTPException(status_code=400, detail="limit_price and base_size must be positive numbers")

    log.info(f"[ORDER] limit_post_only sym={sym} side={side} px={limit_price} qty={base_size}")
    od = _place_limit_post_only(sym, side, str(limit_price), str(base_size))
    order_id = str(od.get("id") or od.get("order_id") or "")
    if not order_id:
        # Some responses may embed order object differently; try to sniff.
        if isinstance(od, dict):
            for k, v in od.items():
                if isinstance(v, dict) and ("id" in v or "order_id" in v):
                    order_id = str(v.get("id") or v.get("order_id"))
                    break
    if not order_id:
        log.warning(f"[ORDER] limit_post_only response missing id: {od}")
        raise HTTPException(status_code=502, detail="limit_post_only: upstream did not return order id")
    return {"order_id": order_id}

@app.delete("/order/{order_id}")
def order_cancel(order_id: str, product_id: str = Query(default=SYMBOL)):
    """
    Cancel a resting order. Idempotent. Returns {"ok": true, "order_id": "..."} on success.
    """
    log.info(f"[ORDER] cancel id={order_id} pid={product_id}")
    try:
        _cancel_order(order_id)
    except HTTPException:
        # pass-through error codes from upstream
        raise
    except Exception as e:
        # Treat unknown errors as 502 to the client
        raise HTTPException(status_code=502, detail=f"cancel failed: {e}")
    return {"ok": True, "order_id": order_id, "product_id": product_id}

@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    log.info(f"[ORDER] market_buy pid={product_id} quote_size={quote_size}")
    return order_market(product_id=product_id, side="BUY", quote_size=quote_size)

@app.post("/order/market")
def order_market(
    product_id: Optional[str] = Query(default=None),
    side: Optional[str] = Query(default=None),
    quote_size: Optional[str] = Query(default=None),
    body: Optional[Dict] = Body(default=None),
):
    if (product_id is None or side is None or quote_size is None) and isinstance(body, dict):
        product_id = product_id or body.get("product_id")
        side = (side or body.get("side"))
        qs = body.get("quote_size")
        quote_size = quote_size or (str(qs) if qs is not None else None)

    if not product_id or not side or not quote_size:
        raise HTTPException(status_code=422, detail="Missing required fields: product_id, side, quote_size (query or JSON body)")

    sym = _normalize_symbol(product_id)
    side = side.upper()
    px = last_price or 0.0
    if px <= 0:
        log.warning("[ORDER] abort: last price unavailable")
        raise HTTPException(status_code=503, detail="Last price unavailable")
    qty = Decimal(quote_size) / Decimal(str(px))
    log.info(f"[ORDER] place sym={sym} side={side} quote={quote_size} est_qty={qty}")
    od = _place_order(sym, side, str(qty))

    order_id = str(od.get("id") or od.get("order_id") or "")
    resp = {
        "order_id": order_id,
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
        trades = _order_trades(order_id)
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("quantity", "0")))
            price = Decimal(str(t.get("price", "0")))
            commission = Decimal(str(t.get("fee", "0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T",
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
        log.info(f"[ORDER] placed id={order_id} status={resp['status']} fills={len(resp['fills'])}")
    except Exception as e:
        log.warning(f"[ORDER] trades fetch error id={order_id} err={e}")
    return resp

@app.get("/order/{order_id}")
def order_get(order_id: str, product_id: str = Query(default=SYMBOL)):
    log.info(f"[ORDER] get id={order_id} pid={product_id}")
    od = _get_order(order_id)
    resp = {
        "order_id": str(od.get("id") or order_id),
        "product_id": product_id,
        "status": "open" if str(od.get("status", "")).lower() in ("new", "partially_filled") else "done",
        "created_at": _now_iso(),
        "filled_size": str(od.get("quantity_cumulative", "0")),
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": str(od.get("side", "")).lower(),
    }
    try:
        trades = _order_trades(order_id)
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("quantity", "0")))
            price = Decimal(str(t.get("price", "0")))
            commission = Decimal(str(t.get("fee", "0")))
            # For limit-maker we mark "M"; market stays "T".
            liq = "M" if str(od.get("type", "")).lower() == "limit" else "T"
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": liq,
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
            resp["status"] = "done" if str(od.get("status", "")).lower() in ("filled", "canceled", "expired", "rejected") else resp["status"]
        log.info(f"[ORDER] get id={order_id} status={resp['status']} fills={len(resp['fills'])}")
    except Exception as e:
        log.warning(f"[ORDER] trades fetch error id={order_id} err={e}")
    return resp

# ---- Runner
async def _runner():
    log.info(f"[BOOT] starting bridge-hitbtc PORT={PORT} SYMBOL={SYMBOL} LOG_LEVEL={LOG_LEVEL} HEARTBEAT_TOUCH_TS={HEARTBEAT_TOUCH_TS} HEARTBEAT_SEC={HEARTBEAT_SEC}")
    task_ws = asyncio.create_task(_ws_loop())
    task_hb = asyncio.create_task(_keepalive_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task_ws, task_hb, srv.serve())

if __name__ == "__main__":
    try:
        asyncio.run(_runner())
    except KeyboardInterrupt:
        pass
