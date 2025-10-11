# FILE: bridge/app.py
import os
import json
from pathlib import Path
from decimal import Decimal
from urllib import request as urlreq
from urllib import parse as urlparse

from fastapi import FastAPI, HTTPException, Query
from pydantic import BaseModel
from coinbase.rest import RESTClient
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, List, Optional, Literal

# Auto-load parent .env: ~/coinbase/.env
try:
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).resolve().parents[1] / ".env")
except Exception:
    pass

# === EXISTING AUTH (unchanged) ===
API_KEY = os.environ["COINBASE_API_KEY_NAME"]               # organizations/.../apiKeys/...
API_SECRET = os.getenv("COINBASE_API_PRIVATE_KEY") or os.getenv("COINBASE_API_SECRET")
if not API_SECRET:
    raise RuntimeError("Missing COINBASE_API_PRIVATE_KEY (or COINBASE_API_SECRET)")
if "\\n" in API_SECRET:
    API_SECRET = API_SECRET.replace("\\n", "\n")

client = RESTClient(api_key=API_KEY, api_secret=API_SECRET)
app = FastAPI(title="coinbase-bridge", version="0.1")

# === EXISTING ROUTES (unchanged) ===

@app.get("/health")
def health():
    return {"ok": True}

from decimal import Decimal
from fastapi import HTTPException

@app.get("/accounts")
def accounts(limit: int = 250):
    try:
        raw = client.get_accounts(limit=limit).to_dict()
    except Exception as e:
        raise HTTPException(status_code=401, detail=str(e))

    # Coinbase Advanced Trade commonly returns:
    # {
    #   "accounts": [
    #     {
    #       "uuid": "...",
    #       "currency": "USDC",
    #       "available_balance": {"value": "12.34", "currency": "USDC"},
    #       "hold": {"value": "1.00", "currency": "USDC"}  # sometimes present
    #       ...
    #     }, ...
    #   ],
    #   "has_next": false, "cursor": "", "size": N
    # }
    #
    # But shapes can vary. We normalize to include:
    # - available_balance (string value)
    # - hold_balance      (string value; from "hold" or 0 if missing)
    # - locked_balance    (alias of hold_balance for clients that look for 'locked')
    # - total_balance     (available + hold, as string)
    # - currency (uppercased)
    # - type, platform (for consistency/debug)

    rows = (raw.get("accounts") or []) if isinstance(raw, dict) else []
    out = []
    for a in rows:
        cur = str(a.get("currency", "")).upper()

        # available (string)
        ab = (a.get("available_balance") or {})
        av = str(ab.get("value", "0"))

        # hold/locked (may be under "hold" or absent)
        hb = (a.get("hold") or a.get("hold_balance") or {})
        hd = str(hb.get("value", "0"))

        # compute total precisely
        total = str(Decimal(av) + Decimal(hd))

        out.append({
            "currency": cur,
            "available_balance": {"value": av, "currency": cur},
            "hold_balance":      {"value": hd, "currency": cur},
            "locked_balance":    {"value": hd, "currency": cur},  # <-- added alias for clients expecting 'locked_balance'
            "total_balance":     {"value": total, "currency": cur},
            "type": "spot",
            "platform": "coinbase",
        })

    return {
        "accounts": out,
        "has_next": bool(raw.get("has_next")) if isinstance(raw, dict) else False,
        "cursor":   str(raw.get("cursor") or ""),
        "size":     len(out),
    }


@app.get("/product/{product_id}")
def product(product_id: str):
    try:
        p = client.get_product(product_id)
        return p.to_dict() if hasattr(p, "to_dict") else p
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

class MarketBuy(BaseModel):
    client_order_id: str
    product_id: str
    quote_size: str

@app.post("/orders/market_buy")
def market_buy(payload: MarketBuy):
    try:
        return client.market_order_buy(
            client_order_id=payload.client_order_id,
            product_id=payload.product_id,
            quote_size=payload.quote_size,
        )
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === NEW/UPDATED: /candles + /order/market ===

# Canonical granularity to seconds (per Coinbase enums)
_GRAN_TO_SECONDS = {
    "ONE_MINUTE": 60,
    "FIVE_MINUTE": 5 * 60,
    "FIFTEEN_MINUTE": 15 * 60,
    "THIRTY_MINUTE": 30 * 60,
    "ONE_HOUR": 60 * 60,
    "TWO_HOUR": 2 * 60 * 60,
    "FOUR_HOUR": 4 * 60 * 60,
    "SIX_HOUR": 6 * 60 * 60,
    "ONE_DAY": 24 * 60 * 60,
}

def _to_unix_s(dt: datetime) -> str:
    return str(int(dt.timestamp()))

def _normalize_candles(raw: Any) -> List[Dict[str, str]]:
    """
    Output: [{"start","open","high","low","close","volume"}, ...] with values as strings.
    Accepts SDK dict/list, or public endpoint response.
    """
    if hasattr(raw, "to_dict"):
        raw = raw.to_dict()

    rows = raw.get("candles") if isinstance(raw, dict) else raw
    if rows is None:
        rows = []

    out: List[Dict[str, str]] = []
    for r in rows:
        if isinstance(r, dict):
            start = str(r.get("start") or r.get("start_time") or r.get("time") or "")
            open_ = str(r.get("open") or "")
            high  = str(r.get("high") or "")
            low   = str(r.get("low") or "")
            close = str(r.get("close") or "")
            vol   = str(r.get("volume") or r.get("vol") or "")
        else:
            # list/tuple [start, open, high, low, close, volume]
            try:
                start, open_, high, low, close, vol = [str(x) for x in r]
            except Exception:
                continue

        out.append({
            "start": start,      # per spec: UNIX seconds (string)
            "open": open_,
            "high": high,
            "low": low,
            "close": close,
            "volume": vol,
        })
    return out

def _call_candles_via_sdk(product_id: str, granularity: str, start_unix: str, end_unix: str, limit: int):
    """
    Use the official SDK with UNIX seconds. Try multiple method/arg shapes.
    """
    methods = []
    for name in ("get_candles", "get_market_candles", "list_candles"):
        if hasattr(client, name):
            methods.append(getattr(client, name))
    if not methods:
        raise RuntimeError("RESTClient has no candle method (expected get_candles/get_market_candles/list_candles)")

    # Per OpenAPI: start/end required; limit max 350
    argsets = [
        {"product_id": product_id, "granularity": granularity, "start": start_unix, "end": end_unix, "limit": limit},
        {"product_id": product_id, "granularity": granularity, "start": start_unix, "end": end_unix},
        {"product_id": product_id, "granularity": granularity, "start_time": start_unix, "end_time": end_unix, "limit": limit},
        {"product_id": product_id, "granularity": granularity, "start_time": start_unix, "end_time": end_unix},
    ]

    last_err: Optional[Exception] = None
    for fn in methods:
        for kwargs in argsets:
            try:
                return fn(**kwargs)
            except TypeError as te:
                last_err = te
                continue
            except Exception as e:
                last_err = e
                continue
    raise last_err or RuntimeError("All candle method attempts failed")

def _call_candles_via_public_http(product_id: str, granularity: str, start_unix: str, end_unix: str, limit: int):
    """
    Fallback to HTTP. Note: the canonical path is /api/v3/brokerage/products/{product_id}/candles.
    This endpoint typically expects auth; this is a last-resort path for environments where it works.
    """
    base = "https://api.coinbase.com"
    path = f"/api/v3/brokerage/products/{urlparse.quote(product_id)}/candles"
    qs = {
        "granularity": granularity,
        "start": start_unix,
        "end": end_unix,
        "limit": str(limit),
    }
    url = f"{base}{path}?{urlparse.urlencode(qs)}"
    req = urlreq.Request(url, headers={"User-Agent": "coinbase-bridge/1.0"})
    with urlreq.urlopen(req, timeout=15) as resp:
        body = resp.read()
        if resp.status >= 300:
            raise HTTPException(status_code=resp.status, detail=f"public candles error: {body.decode('utf-8', 'ignore')}")
    data = json.loads(body.decode("utf-8", "ignore"))
    return _normalize_candles(data)

@app.get("/candles")
def candles(
    product_id: str = Query(..., description="e.g., BTC-USD"),
    granularity: str = Query("ONE_MINUTE", description="ONE_MINUTE, FIVE_MINUTE, FIFTEEN_MINUTE, THIRTY_MINUTE, ONE_HOUR, TWO_HOUR, FOUR_HOUR, SIX_HOUR, ONE_DAY"),
    limit: int = Query(300, ge=1, le=350),  # clamp to API max 350
    start: Optional[str] = Query(None, description="UNIX seconds (string). If omitted, window is inferred."),
    end: Optional[str]   = Query(None, description="UNIX seconds (string). If omitted, now is used."),
):
    """
    Returns normalized OHLCV list. If start/end are omitted, infer a window of
    (limit × granularity) ending at 'now' per API requirements. All times
    sent to Coinbase are **UNIX seconds** (strings), matching the spec.
    """
    try:
        sec = _GRAN_TO_SECONDS.get(granularity, 60)

        # Build time window in UTC
        if end and end.strip().isdigit():
            end_dt = datetime.fromtimestamp(int(end.strip()), tz=timezone.utc)
        else:
            end_dt = datetime.now(timezone.utc)

        if start and start.strip().isdigit():
            start_dt = datetime.fromtimestamp(int(start.strip()), tz=timezone.utc)
        else:
            # add a small buffer (+2 buckets) to avoid edge truncation
            start_dt = end_dt - timedelta(seconds=sec * max(1, min(limit + 2, 350)))

        # Ensure start < end
        if start_dt >= end_dt:
            start_dt = end_dt - timedelta(seconds=sec * max(1, min(limit, 350)))

        start_unix = _to_unix_s(start_dt)
        end_unix   = _to_unix_s(end_dt)

        # SDK first (auth), fallback HTTP (best-effort)
        try:
            raw = _call_candles_via_sdk(product_id, granularity, start_unix, end_unix, limit)
            return _normalize_candles(raw)
        except Exception:
            raw = _call_candles_via_public_http(product_id, granularity, start_unix, end_unix, limit)
            return raw
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# ---- /order/market (unified) ----

class MarketOrder(BaseModel):
    product_id: str
    side: Literal["BUY", "SELL"]
    quote_size: str
    client_order_id: Optional[str] = None

def _get_price(product_id: str) -> Decimal:
    p = client.get_product(product_id)
    if hasattr(p, "to_dict"):
        p = p.to_dict()
    price_str = str(p.get("price") or p.get("mid_market_price") or p.get("ask") or p.get("bid") or "")
    if not price_str:
        raise RuntimeError("Could not retrieve product price")
    return Decimal(price_str)

@app.post("/order/market")
def order_market(payload: MarketOrder):
    """
    Unified market order by quote_size (USD), side = BUY|SELL.
    For SELL, if the SDK rejects quote_size, fallback to base_size using current price.
    """
    try:
        if payload.side == "BUY":
            return client.market_order_buy(
                client_order_id=payload.client_order_id,
                product_id=payload.product_id,
                quote_size=payload.quote_size,
            )

        # SELL path
        if hasattr(client, "market_order_sell"):
            try:
                return client.market_order_sell(
                    client_order_id=payload.client_order_id,
                    product_id=payload.product_id,
                    quote_size=payload.quote_size,
                )
            except Exception:
                price = _get_price(payload.product_id)
                base_size = (Decimal(payload.quote_size) / price).quantize(Decimal("0.00000001"))
                return client.market_order_sell(
                    client_order_id=payload.client_order_id,
                    product_id=payload.product_id,
                    base_size=str(base_size),
                )

        if hasattr(client, "place_market_order"):
            return client.place_market_order(
                client_order_id=payload.client_order_id,
                product_id=payload.product_id,
                side="SELL",
                quote_size=payload.quote_size,
            )

        raise RuntimeError("RESTClient missing market_order_sell/place_market_order")
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === INCREMENTAL ADDITIONS: optional WS ticker + /price endpoint (opt-in) ===

# These additions are fully optional and do not change existing behavior unless enabled via env:
#   COINBASE_WS_ENABLE=true
#   COINBASE_WS_PRODUCTS=BTC-USD[,ETH-USD,...]
#   COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com
#   COINBASE_WS_STALE_SEC=10
import asyncio
import time
try:
    import websockets  # type: ignore
except Exception:
    websockets = None  # degrade gracefully if package not present

WS_ENABLE = os.getenv("COINBASE_WS_ENABLE", "false").lower() == "true"
WS_URL = os.getenv("COINBASE_WS_URL", "wss://advanced-trade-ws.coinbase.com")
WS_PRODUCTS = [p.strip() for p in os.getenv("COINBASE_WS_PRODUCTS", "BTC-USD").split(",") if p.strip()]
WS_STALE_SEC = int(os.getenv("COINBASE_WS_STALE_SEC", "10"))

_last_ticks: Dict[str, Dict[str, float]] = {}  # {"BTC-USD": {"price": 12345.67, "ts": 1690000000.0}}
_ws_task: Optional[asyncio.Task] = None

async def _ws_consume():
    if not WS_ENABLE:
        return
    if websockets is None:
        print("[bridge] websockets not installed; WS disabled")
        return
    # Coinbase Advanced Trade WS subscribe format
    payload = {"type": "subscribe", "channel": "ticker", "product_ids": WS_PRODUCTS}
    backoff = 1
    while True:
        try:
            async with websockets.connect(WS_URL, ping_interval=20, ping_timeout=20) as ws:
                await ws.send(json.dumps(payload))
                print(f"[bridge] WS connected: {WS_URL} products={WS_PRODUCTS}")
                backoff = 1
                async for msg in ws:
                    try:
                        ev = json.loads(msg)
                        pid = ev.get("product_id") or ev.get("productId") or ev.get("product")
                        price = ev.get("price") or ev.get("last_trade_price") or ev.get("best_ask") or ev.get("best_bid")
                        # If top-level keys missing, try event-wrapped payloads
                        if not (pid and price):
                            events = ev.get("events") or []
                            if events and isinstance(events, list):
                                e0 = events[0]
                                if isinstance(e0, dict) and "tickers" in e0 and e0["tickers"]:
                                    t0 = e0["tickers"][0]
                                    pid = t0.get("product_id") or t0.get("productId")
                                    price = t0.get("price")
                        if pid and price:
                            try:
                                p = float(price)
                                ts = time.time()
                                _last_ticks[pid] = {"price": p, "ts": ts}
                            except Exception:
                                pass
                    except Exception:
                        continue
        except Exception as e:
            print(f"[bridge] WS error: {e}; reconnecting in {backoff}s")
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 30)

@app.on_event("startup")
async def _startup_ws():
    if WS_ENABLE and websockets is not None:
        loop = asyncio.get_event_loop()
        global _ws_task
        if _ws_task is None:
            _ws_task = loop.create_task(_ws_consume())
            print(f"[bridge] WS ticker enabled; products={WS_PRODUCTS}")

@app.get("/price")
def price(product_id: str = Query(..., alias="product_id")):
    rec = _last_ticks.get(product_id)
    if not rec:
        return {"error": "no_tick", "product_id": product_id}
    ts = rec["ts"]
    stale = (time.time() - ts) > WS_STALE_SEC
    t_iso = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts))
    return {"product_id": product_id, "price": rec["price"], "ts": t_iso, "stale": stale}

# === MINIMAL ADDITION: GET /order/{order_id} (fills/status summary) ===

@app.get("/order/{order_id}")
def get_order(order_id: str):
    """
    Return a minimal summary for an order:
      - status (if available from fills payload)
      - filled_size (sum of fill sizes, **BASE units**)
      - average_filled_price (size-weighted)
      - commission_total_usd (sum of fills[].commission)
    Notes:
      * Uses the official SDK's fills method(s) and is robust to naming differences.
      * If no fills are found, returns zeros and status 'UNKNOWN'.
    """
    try:
        # Try available fills methods on the SDK
        methods = [name for name in ("get_fills", "list_fills") if hasattr(client, name)]
        fills: List[Dict[str, Any]] = []
        last_err: Optional[Exception] = None

        for m in methods:
            try:
                resp = getattr(client, m)(order_id=order_id)
                data = resp.to_dict() if hasattr(resp, "to_dict") else resp
                arr = None
                if isinstance(data, dict):
                    # Common shapes: {"fills":[...]} or {"data":[...]} or {"results":[...]}
                    arr = data.get("fills") or data.get("data") or data.get("results")
                if isinstance(arr, list):
                    fills = arr
                    break
            except Exception as e:
                last_err = e
                continue

        # If no fills were returned, report UNKNOWN with zeros
        if not fills:
            return {
                "order_id": order_id,
                "status": "UNKNOWN",
                "filled_size": "0",
                "average_filled_price": "0",
                "commission_total_usd": "0"
            }

        total_base = Decimal("0")
        total_notional = Decimal("0")
        total_commission = Decimal("0")
        status = "UNKNOWN"

        for f in fills:
            # price
            price_str = str(f.get("price") or f.get("average_filled_price") or "0")
            try:
                price = Decimal(price_str)
            except Exception:
                price = Decimal("0")

            # size may be base or quote; Coinbase provides a size_in_quote flag
            size_str = str(f.get("size") or f.get("filled_size") or "0")
            try:
                size = Decimal(size_str)
            except Exception:
                size = Decimal("0")

            # commission (USD)
            commission_str = str(f.get("commission") or "0")
            try:
                commission = Decimal(commission_str)
            except Exception:
                commission = Decimal("0")
            total_commission += commission

            size_in_quote = bool(f.get("size_in_quote"))
            if size_in_quote:
                base = (size / price) if price > 0 else Decimal("0")
                notional = size
            else:
                base = size
                notional = size * price

            total_base += base
            total_notional += notional

            st = f.get("order_status") or f.get("status")
            if isinstance(st, str) and st:
                status = st

        avg_price = (total_notional / total_base) if total_base > 0 else Decimal("0")

        return {
            "order_id": order_id,
            "status": status,
            "filled_size": format(total_base, "f"),              # BASE units
            "average_filled_price": format(avg_price, "f"),
            "commission_total_usd": format(total_commission, "f"),
        }
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === NEW: balances for base/quote (strings: asset, available, step) ===

def _product_symbols_and_steps(product_id: str):
    """
    Return (base, quote, base_step, quote_step) strings from the product payload.
    Any missing step returns "0" so the Go side can apply its env override.
    """
    p = client.get_product(product_id)
    if hasattr(p, "to_dict"):
        p = p.to_dict()
    base  = str(p.get("base_currency")  or p.get("base_currency_id")  or p.get("base")  or p.get("base_display_symbol")  or "")
    quote = str(p.get("quote_currency") or p.get("quote_currency_id") or p.get("quote") or p.get("quote_display_symbol") or "")
    base_step  = str(p.get("base_increment")  or p.get("base_increment_value")  or "0")
    quote_step = str(p.get("quote_increment") or p.get("quote_increment_value") or "0")
    return base, quote, base_step, quote_step

def _sum_available(currency: str) -> str:
    """
    Sum available balances for a currency across all accounts; return as decimal string.
    Looks specifically at 'available_balance': {'currency': 'BTC', 'value': '...'}.
    """
    if not currency:
        return "0"
    try:
        resp = client.get_accounts(limit=200)
        data = resp.to_dict() if hasattr(resp, "to_dict") else resp
        accounts = data.get("accounts") or data.get("data") or []
        total = Decimal("0")
        for a in accounts:
            ab = a.get("available_balance") or {}
            if str(ab.get("currency") or "") == currency:
                try:
                    total += Decimal(str(ab.get("value") or "0"))
                except Exception:
                    pass
        return format(total, "f")
    except Exception:
        return "0"

@app.get("/balance/base")
def balance_base(product_id: str = Query(..., description="e.g., BTC-USD")):
    """
    Shape: {"asset":"BTC","available":"0.00000000","step":"0.00000001"}
    """
    try:
        base, _quote, base_step, _quote_step = _product_symbols_and_steps(product_id)
        if not base:
            raise RuntimeError("could not resolve base currency for product")
        return {"asset": base, "available": _sum_available(base), "step": base_step or "0"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(..., description="e.g., BTC-USD")):
    """
    Shape: {"asset":"USD","available":"0.00","step":"0.01"}
    """
    try:
        _base, quote, _base_step, quote_step = _product_symbols_and_steps(product_id)
        if not quote:
            raise RuntimeError("could not resolve quote currency for product")
        return {"asset": quote, "available": _sum_available(quote), "step": quote_step or "0"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === NEW: Post-only limit order + cancel endpoint ===

class LimitPostOnly(BaseModel):
    product_id: str
    side: Literal["BUY", "SELL"]
    limit_price: str    # accept as string to preserve precision
    base_size: str      # accept as string to preserve precision
    client_order_id: Optional[str] = None
    time_in_force: Optional[Literal["GTC", "IOC", "FOK"]] = "GTC"  # default GTC

@app.post("/order/limit_post_only")
def order_limit_post_only(payload: LimitPostOnly):
    """
    Place a post-only limit order (maker-only). Returns whatever the SDK returns,
    but we try to ensure an 'order_id' field is present for client convenience.
    """
    try:
        kwargs = dict(
            client_order_id=payload.client_order_id,
            product_id=payload.product_id,
            limit_price=payload.limit_price,
            base_size=payload.base_size,
        )
        # Prefer explicit post_only/time_in_force flags when available
        # Try multiple method shapes to be robust to SDK versions.
        candidates = []

        if hasattr(client, "limit_order_buy") and payload.side == "BUY":
            def _buy():
                return client.limit_order_buy(post_only=True, time_in_force=payload.time_in_force or "GTC", **kwargs)
            candidates.append(_buy)

        if hasattr(client, "limit_order_sell") and payload.side == "SELL":
            def _sell():
                return client.limit_order_sell(post_only=True, time_in_force=payload.time_in_force or "GTC", **kwargs)
            candidates.append(_sell)

        if hasattr(client, "place_limit_order"):
            def _generic():
                return client.place_limit_order(
                    client_order_id=payload.client_order_id,
                    product_id=payload.product_id,
                    side=payload.side,
                    limit_price=payload.limit_price,
                    base_size=payload.base_size,
                    post_only=True,
                    time_in_force=payload.time_in_force or "GTC",
                )
            candidates.append(_generic)

        last_err: Optional[Exception] = None
        for fn in candidates:
            try:
                resp = fn()
                out = resp.to_dict() if hasattr(resp, "to_dict") else resp
                # Best-effort: ensure top-level order_id if possible
                if isinstance(out, dict):
                    oid = out.get("order_id") or out.get("orderId")
                    if not oid:
                        sr = out.get("success_response") or out.get("successResponse") or {}
                        oid = sr.get("order_id") or sr.get("orderId")
                        if oid:
                            out["order_id"] = oid
                return out
            except Exception as e:
                last_err = e
                continue

        raise last_err or RuntimeError("No suitable limit order method found on RESTClient")
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

@app.get("/exchange/filters")
def exchange_filters(product_id: str):
    """
    Return Binance-style filters for Coinbase products so the Go broker can snap
    size/price before submission.
      - step_size  := base_increment (quantity lot size)
      - tick_size  := quote_increment (price tick size)
    """
    try:
        _base, _quote, base_step, quote_step = _product_symbols_and_steps(product_id)
        # Ensure non-empty strings; broker treats 0 as "unavailable"
        step = str(base_step or "0").strip()
        tick = str(quote_step or "0").strip()
        if not step or step == "0":
            raise RuntimeError("missing base_increment")
        if not tick or tick == "0":
            raise RuntimeError("missing quote_increment")
        return {"product_id": product_id, "step_size": step, "tick_size": tick}
    except Exception as e:
        # Match your broker expectation: non-2xx yields an error
        from fastapi import HTTPException
        raise HTTPException(status_code=404, detail=str(e))

@app.delete("/order/{order_id}")
def cancel_order(order_id: str, product_id: Optional[str] = Query(None)):
    """
    Request cancellation of a resting order. Returns a small acknowledgment.
    Tries multiple SDK shapes to accommodate version differences.
    """
    try:
        # Try batch-style cancels first if present
        tried = False
        if hasattr(client, "cancel_orders"):
            tried = True
            try:
                resp = client.cancel_orders(order_ids=[order_id])
                out = resp.to_dict() if hasattr(resp, "to_dict") else resp
                return {"order_id": order_id, "status": "cancel_requested", "response": out}
            except Exception:
                pass

        # Some SDKs expose cancel_order(order_id=..., product_id=...)
        if hasattr(client, "cancel_order"):
            tried = True
            try:
                resp = client.cancel_order(order_id=order_id, product_id=product_id)
                out = resp.to_dict() if hasattr(resp, "to_dict") else resp
                return {"order_id": order_id, "status": "cancel_requested", "response": out}
            except Exception:
                pass

        if not tried:
            raise RuntimeError("No cancel method on RESTClient")

        # If we reached here, cancellation call(s) failed but we still return best-effort ack.
        return {"order_id": order_id, "status": "cancel_attempted"}
    except Exception as e:
        raise HTTPException(sta
