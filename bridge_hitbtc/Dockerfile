FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

RUN pip install --no-cache-dir fastapi "uvicorn[standard]" websockets python-dotenv

WORKDIR /app
COPY ws_hitbtc.py /app/ws_hitbtc.py

ENV SYMBOL=BTCUSDT PORT=8788 STALE_MS=3000
EXPOSE 8788
CMD ["uvicorn", "ws_hitbtc:app", "--host", "0.0.0.0", "--port", "8788"]
