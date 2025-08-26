FROM python:3.11-slim

# tools for manual testing
RUN apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/*

WORKDIR /app/bridge

# install Python deps from your existing requirements.txt
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# copy the FastAPI app (safe even if you also mount it as a volume)
COPY . .

EXPOSE 8787
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8787"]
