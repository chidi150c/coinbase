import os
from pathlib import Path
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from coinbase.rest import RESTClient

# Auto-load parent .env: ~/coinbase/.env
try:
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).resolve().parents[1] / ".env")
except Exception:
    pass

# Accept either var name for the PEM (my .env currently uses COINBASE_API_SECRET)
API_KEY = os.environ["COINBASE_API_KEY_NAME"]               # organizations/.../apiKeys/...
API_SECRET = os.getenv("COINBASE_API_PRIVATE_KEY") or os.getenv("COINBASE_API_SECRET")
if not API_SECRET:
    raise RuntimeError("Missing COINBASE_API_PRIVATE_KEY (or COINBASE_API_SECRET)")

# Normalize single-line \n to real newlines
if "\\n" in API_SECRET:
    API_SECRET = API_SECRET.replace("\\n", "\n")

client = RESTClient(api_key=API_KEY, api_secret=API_SECRET)
app = FastAPI(title="coinbase-bridge", version="0.1")

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
