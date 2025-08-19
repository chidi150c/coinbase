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

@app.get("/accounts")
def accounts(limit: int = 1):
    try:
        return client.get_accounts(limit=limit).to_dict()
    except Exception as e:
        raise HTTPException(status_code=401, detail=str(e))

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
