# bridge/Dockerfile.ws  (new WS bridge used by binance & hitbtc)
FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

RUN pip install --no-cache-dir fastapi "uvicorn[standard]" websockets

WORKDIR /app
COPY bridge_ws.py /app/bridge_ws.py

# Defaults (overridden per service)
ENV EXCHANGE=hitbtc SYMBOL=BTCUSDT PORT=8788 STALE_MS=3000
EXPOSE 8788
CMD ["python", "/app/bridge_ws.py"]
