#!/usr/bin/env python3
import os, asyncio, json, time, hmac, hashlib
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq
from urllib import parse as urlparse

from fastapi import FastAPI, Query, HTTPException
from fastapi.responses import JSONResponse
import uvicorn
import websockets

# === Config (mirrors coinbase style envs) ===
SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8789"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

BINANCE_API_KEY     = os.getenv("BINANCE_API_KEY", "").strip()
BINANCE_API_SECRET  = os.getenv("BINANCE_API_SECRET", "").strip()
BINANCE_BASE_URL    = os.getenv("BINANCE_BASE_URL", "https://api.binance.com").strip().rstrip("/")
BINANCE_RECV_WINDOW = os.getenv("BINANCE_RECV_WINDOW", "5000").strip()

# === In-memory state (mirror Coinbase JSON shapes) ===
last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}  # minuteStartMs -> [o,h,l,c,vol]

def _now_ms() -> int: return int(time.time() * 1000)
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

async def _ws_loop_binance():
    """Consume Binance combined streams: bookTicker + trades for SYMBOL."""
    global last_price, last_ts_ms
    sym = SYMBOL.lower()
    url = f"wss://stream.binance.com:9443/stream?streams={sym}@bookTicker/{sym}@trade"
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
                    stream = msg.get("stream", "")
                    data   = msg.get("data", {})
                    px: Optional[float] = None
                    if stream.endswith("@bookTicker"):
                        a = data.get("a"); b = data.get("b")
                        try:
                            if a and b: px = (float(a)+float(b))/2.0
                        except Exception: px = None
                    elif stream.endswith("@trade"):
                        p = data.get("p")
                        try:
                            if p: px = float(p)
                        except Exception: px = None
                    if px and px > 0:
                        last_price = px
                        last_ts_ms = ts
                        _update_candle(px, ts)
        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

# --- HTTP app (mirror Coinbase) ---
app = FastAPI(title="bridge-binance", version="0.1")

@app.get("/health")
def health():
    return {"ok": True}

@app.get("/price")
def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None; stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms() - last_ts_ms)
        stale  = age_ms > STALE_MS
    return JSONResponse({"product_id": product_id, "price": float(last_price) if last_price else 0.0, "ts": last_ts_ms, "stale": stale})

@app.get("/candles")
def candles_between(product_id: str = Query(default=SYMBOL),
                    granularity: str = Query(default="ONE_MINUTE"),
                    start: Optional[int] = Query(default=None),
                    end: Optional[int] = Query(default=None),
                    limit: int = Query(default=350)):
    if granularity != "ONE_MINUTE":
        return JSONResponse({"candles": []})
    keys = sorted(k for k in candles.keys() if (start is None or k >= start) and (end is None or k <= end))
    rows = []
    for k in keys[-limit:]:
        o,h,l,c,v = candles[k]
        rows.append({"start": str(k//1000), "open": str(o), "high": str(h), "low": str(l), "close": str(c), "volume": str(v)})
    return JSONResponse({"candles": rows})

# --- Accounts & balances (mirror Coinbase JSON) ---
def _binance_signed_request(path: str, params: Dict[str,str]) -> Dict:
    if not BINANCE_API_KEY or not BINANCE_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing BINANCE_API_KEY/SECRET")
    params = dict(params or {})
    params.setdefault("timestamp", str(_now_ms()))
    params.setdefault("recvWindow", BINANCE_RECV_WINDOW)
    q = urlparse.urlencode(params, doseq=True)
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

@app.get("/accounts")
def accounts(limit: int = 250):
    payload = _binance_signed_request("/api/v3/account", {})
    bals = payload.get("balances") or []
    out = []
    for b in bals:
        asset = str(b.get("asset", "")).upper()
        free  = str(b.get("free", "0"))
        out.append({
            "currency": asset,
            "available_balance": {"value": free, "currency": asset},
            "type": "spot",
            "platform": "binance",
        })
    return JSONResponse({"accounts": out, "has_next": False, "cursor": "", "size": len(out)})

def _split_product(pid: str) -> Tuple[str,str]:
    p = pid.upper().replace("-", "")
    # Prefer common stable quotes
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

@app.get("/balance/base")
def balance_base(product_id: str = Query(..., description="e.g., BTCUSDT")):
    base, _ = _split_product(product_id)
    data = accounts().body
    payload = json.loads(data) if isinstance(data, (bytes, bytearray)) else data
    value = _sum_available(payload.get("accounts",[]), base)
    # step unknown for Binance via this bridge; mirror coinbase field name
    return {"asset": base, "available": value, "step": "0"}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(..., description="e.g., BTCUSDT")):
    _, quote = _split_product(product_id)
    data = accounts().body
    payload = json.loads(data) if isinstance(data, (bytes, bytearray)) else data
    value = _sum_available(payload.get("accounts",[]), quote)
    return {"asset": quote, "available": value, "step": "0"}

# --- Runner ---
async def _runner():
    task = asyncio.create_task(_ws_loop_binance())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass
