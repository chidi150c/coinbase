#!/usr/bin/env python3
# FILE: bridge_binance/ws_binance.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using Binance WS/REST.
import os, asyncio, json, time, hmac, hashlib, logging
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq, parse as urlparse
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path
from fastapi.responses import JSONResponse
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
    global last_price, last_ts_ms
    sym = _normalize_symbol(SYMBOL).lower()
    url = f"wss://stream.binance.com:9443/stream?streams={sym}@bookTicker/{sym}@trade"
    log.info(f"[TRACE] WS subscribe {url}")
    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
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
                    px = None
                    if stream.endswith("@bookTicker"):
                        a = data.get("a"); b = data.get("b")
                        try:
                            if a is not None and b is not None:
                                px = (float(a)+float(b))/2.0
                        except Exception:
                            px = None
                    elif stream.endswith("@trade"):
                        p = data.get("p")
                        try:
                            if p is not None: px = float(p)
                        except Exception:
                            px = None
                    if px and px > 0:
                        last_price = px
                        last_ts_ms = ts
                        _update_candle(px, ts)
        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

# ---- FastAPI app (mirrors Coinbase endpoints) ----
app = FastAPI(title="bridge-binance", version="0.2")

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
def get_candles(product_id: str = Query(default=SYMBOL),
           granularity: str = Query(default="ONE_MINUTE"),
           start: Optional[int] = Query(default=None),
           end: Optional[int] = Query(default=None),
           limit: int = Query(default=350)):
    if granularity != "ONE_MINUTE":
        return {"candles": []}
    # --- minimal change: auto-detect seconds vs milliseconds for start/end ---
    def _ms(x: Optional[int]) -> Optional[int]:
        if x is None: return None
        # treat values < 10^12 as seconds; convert to ms
        return x * 1000 if x < 1_000_000_000_000 else x
    s_ms = _ms(start)
    e_ms = _ms(end)

    # 1) Serve from in-memory if available
    keys = sorted(k for k in candles.keys() if (s_ms is None or k >= s_ms) and (e_ms is None or k <= e_ms))
    rows: List[Dict[str,str]] = []
    for k in keys[-limit:]:
        o,h,l,c,v = candles[k]
        rows.append({"start": str(k//1000), "open": str(o), "high": str(h), "low": str(l), "close": str(c), "volume": str(v)})
    if rows:
        return {"candles": rows}

    # 2) Backfill via Binance klines (public)
    sym = _normalize_symbol(product_id)
    interval = "1m"  # ONE_MINUTE
    qs = {"symbol": sym, "interval": interval, "limit": str(min(max(1, limit), 1000))}
    if s_ms is not None: qs["startTime"] = str(s_ms)
    if e_ms is not None: qs["endTime"]   = str(e_ms)
    url = f"{BINANCE_BASE_URL}/api/v3/klines?{urlparse.urlencode(qs)}"
    try:
        data = _http_get(url)  # returns list
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"binance klines error: {e}")

    out = []
    for r in data[-limit:]:
        # r = [ openTime, open, high, low, close, volume, closeTime, ... ]
        try:
            ot = int(r[0])//1000
            o,h,l,c,v = str(r[1]), str(r[2]), str(r[3]), str(r[4]), str(r[5])
        except Exception:
            continue
        out.append({"start": str(ot), "open": o, "high": h, "low": l, "close": c, "volume": v})
        # seed in-memory so future calls have data immediately
        try:
            close_time_ms = int(r[6])
            _update_candle(float(c), close_time_ms)
        except Exception:
            pass
    log.info(f"[TRACE] REST klines fetched: symbol={sym} interval={interval} rows={len(out)}")
    return {"candles": out}

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
    # Minimal parity: return latest price with same shape used by Go client
    # (Coinbase bridge exposes more metadata; we surface price+stale here.)
    return price(product_id)

# --- Order endpoints (market by quote size), with partial-fill enrichment ---

def _my_trades(symbol: str, order_id: int) -> List[Dict]:
    # https://binance-docs.github.io/apidocs/spot/en/#account-trade-list-user_data
    return _binance_signed("/api/v3/myTrades", {"symbol": symbol, "orderId": str(order_id)})

@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    # Delegate to single market endpoint (Coinbase bridge exposes both)
    return order_market(product_id=product_id, side="BUY", quote_size=quote_size)

@app.post("/order/market")
def order_market(product_id: str = Query(...), side: str = Query(...), quote_size: str = Query(...)):
    # Place market order using quote as notional (Binance uses quoteOrderQty)
    sym = product_id.upper().replace("-", "")
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

    # Enrich via myTrades (fills)
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
    sym = product_id.upper().replace("-", "")
    od = _binance_signed("/api/v3/order", {"symbol": sym, "orderId": order_id})
    # Normalize minimal Coinbase-like order view + enrich with trades
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
    task = asyncio.create_task(_ws_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass
