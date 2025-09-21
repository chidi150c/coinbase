# Lightweight Python image
FROM python:3.11-slim

# Disable .pyc and enable unbuffered logs
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

# Install runtime deps (FastAPI server + websockets client)
RUN pip install --no-cache-dir fastapi uvicorn[standard] websockets

# Copy the app
COPY ws_binance.py /app/ws_binance.py

# Optional/documentary; actual port is controlled by $PORT env
EXPOSE 8789

# Run the bridge
CMD ["python", "-u", "/app/ws_binance.py"]
