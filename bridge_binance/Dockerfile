FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

RUN pip install --no-cache-dir fastapi "uvicorn[standard]" websockets python-dotenv

WORKDIR /app
COPY ws_binance.py /app/ws_binance.py

ENV SYMBOL=BTCUSDT PORT=8789 STALE_MS=3000
EXPOSE 8789
CMD ["uvicorn", "ws_binance:app", "--host", "0.0.0.0", "--port", "8789"]
