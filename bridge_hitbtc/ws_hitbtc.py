#!/usr/bin/env python3
# FILE: bridge_hitbtc/ws_hitbtc.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using HitBTC WS/REST.
import os, asyncio, json, time, base64
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path
from fastapi.responses import JSONResponse
import websockets
import uvicorn

SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8788"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

HITBTC_API_KEY   = os.getenv("HITBTC_API_KEY", "").strip()
HITBTC_API_SECRET= os.getenv("HITBTC_API_SECRET", "").strip()
HITBTC_BASE_URL  = os.getenv("HITBTC_BASE_URL", "https://api.hitbtc.com").strip().rstrip("/")

last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}

def _now_ms() -> int: return int(time.time()*1000)
def _now_iso() -> str: return datetime.utcnow().replace(tzinfo=timezone.utc).isoformat()
def _minute_start(ts_ms: int) -> int: return (ts_ms//60000)*60000

def _trim_old(max_minutes=6000):
    if len(candles) <= max_minutes: return
    for k in sorted(candles.keys())[:-max_minutes]:
        candles.pop(k,None)

def _update_candle(px: float, ts_ms: int, vol: float = 0.0):
    m = _minute_start(ts_ms)
    if m not in candles:
        candles[m] = [px,px,px,px,vol]
    else:
        o,h,l,c,v = candles[m]
        if px>h: h=px
        if px<l: l=px
        candles[m] = [o,h,l,px,v+vol]
    _trim_old()

def _req(path: str, method="GET", body: Optional[bytes]=None) -> Dict:
    if not HITBTC_API_KEY or not HITBTC_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing HITBTC_API_KEY/SECRET")
    url = f"{HITBTC_BASE_URL}{path}"
    token = base64.b64encode(f"{HITBTC_API_KEY}:{HITBTC_API_SECRET}".encode("utf-8")).decode("ascii")
    headers = {"Authorization": f"Basic {token}"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urlreq.Request(url, method=method, data=body, headers=headers)
    with urlreq.urlopen(req, timeout=10) as resp:
        data = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=data[:200])
        try:
            return json.loads(data)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON from HitBTC")

def _split(pid: str) -> Tuple[str,str]:
    p = pid.upper().replace("-", "")
    # HitBTC typically uses BTCUSDT as well
    if len(p)>3: return p[:-3], p[-3:]
    return p, "USD"

def _sum_available(accts: List[Dict], asset: str) -> str:
    a = asset.upper()
    for r in accts:
        if r.get("currency","").upper()==a:
            return str(r.get("available_balance",{}).get("value","0"))
    return "0"

async def _ws_loop():
    global last_price, last_ts_ms
    url = "wss://api.hitbtc.com/api/3/ws/public"
    sub = {"method":"subscribe","ch":"trades","params":{"symbol":[SYMBOL]}}
    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
                await ws.send(json.dumps(sub))
                backoff = 1
                while True:
                    raw = await ws.recv()
                    ts = _now_ms()
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        continue
                    if msg.get("ch")=="trades" and "data" in msg:
                        for d in msg["data"]:
                            if d.get("t")==SYMBOL:
                                try:
                                    px = float(d.get("p"))
                                except Exception:
                                    px = None
                                if px and px>0:
                                    last_price = px
                                    last_ts_ms = ts
                                    _update_candle(px, ts)
        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

app = FastAPI(title="bridge-hitbtc", version="0.2")

@app.get("/health")
def health(): return {"ok": True}

@app.get("/price")
def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None; stale=True
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
        return {"candles":[]}
    keys = sorted(k for k in candles.keys() if (start is None or k>=start) and (end is None or k<=end))
    rows=[]
    for k in keys[-limit:]:
        o,h,l,c,v = candles[k]
        rows.append({"start": str(k//1000), "open": str(o), "high": str(h), "low": str(l), "close": str(c), "volume": str(v)})
    return {"candles": rows}

@app.get("/accounts")
def accounts(limit: int = 250):
    # HitBTC: GET /api/3/spot/balance
    payload = _req("/api/3/spot/balance")
    rows = payload if isinstance(payload, list) else payload.get("balance", [])
    out=[]
    for r in rows:
        cur = str(r.get("currency","")).upper()
        avail = str(r.get("available", r.get("cash","0")))
        out.append({
            "currency": cur,
            "available_balance": {"value": avail, "currency": cur},
            "type": "spot",
            "platform": "hitbtc",
        })
    return {"accounts": out, "has_next": False, "cursor": "", "size": len(out)}

@app.get("/balance/base")
def balance_base(product_id: str = Query(...)):
    base, _ = _split(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], base)
    return {"asset": base, "available": value, "step": "0"}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(...)):
    _, quote = _split(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], quote)
    return {"asset": quote, "available": value, "step": "0"}

@app.get("/product/{product_id}")
def product_info(product_id: str = Path(...)):
    # Minimal parity: return price view as Coinbase broker expects
    return price(product_id)

# --- Orders (market), partial-fill enrichment ---
# HitBTC v3 spot order create: POST /api/3/spot/order { "symbol": "...", "side": "buy|sell", "type":"market", "quantity": "..."} or quote notional via "strictValidate": false + "cost"?
# We will implement market-by-quote as: compute quantity = quote / last_price (best-effort) and place type=market.
def _place_order(symbol: str, side: str, quantity: str) -> Dict:
    body = json.dumps({"symbol": symbol, "side": side.lower(), "type":"market", "quantity": quantity}).encode("utf-8")
    return _req("/api/3/spot/order", method="POST", body=body)

def _get_order(order_id: str) -> Dict:
    # HitBTC v3: GET /api/3/spot/order/{order_id}
    return _req(f"/api/3/spot/order/{order_id}")

def _order_trades(order_id: str) -> List[Dict]:
    # HitBTC v3: GET /api/3/spot/order/{order_id}/trades
    payload = _req(f"/api/3/spot/order/{order_id}/trades")
    return payload if isinstance(payload, list) else payload.get("trades", [])

@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    return order_market(product_id=product_id, side="BUY", quote_size=quote_size)

@app.post("/order/market")
def order_market(product_id: str = Query(...), side: str = Query(...), quote_size: str = Query(...)):
    sym = product_id.upper().replace("-", "")
    side = side.upper()
    # Approximate quantity from last price (HitBTC doesn't natively support quote notional in v3)
    px = last_price or 0.0
    if px <= 0:
        raise HTTPException(status_code=503, detail="Last price unavailable")
    qty = Decimal(quote_size) / Decimal(str(px))
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

    # Enrich with trades
    try:
        trades = _order_trades(order_id)
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("quantity","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("fee","0")))
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
    except Exception:
        pass
    return resp

@app.get("/order/{order_id}")
def order_get(order_id: str, product_id: str = Query(default=SYMBOL)):
    od = _get_order(order_id)
    sym = product_id.upper().replace("-", "")
    # Basic normalization + enrichment
    resp = {
        "order_id": str(od.get("id") or order_id),
        "product_id": product_id,
        "status": "open" if str(od.get("status","")).lower() in ("new","partially_filled") else "done",
        "created_at": _now_iso(),
        "filled_size": str(od.get("quantity_cumulative","0")),
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": str(od.get("side","")).lower(),
    }
    try:
        trades = _order_trades(order_id)
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills=[]
        for t in trades:
            qty = Decimal(str(t.get("quantity","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("fee","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty*price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done" if str(od.get("status","")).lower() in ("filled","canceled","expired","rejected") else resp["status"]
    except Exception:
        pass
    return resp

async def _runner():
    task = asyncio.create_task(_ws_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass
