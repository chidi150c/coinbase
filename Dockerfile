# ---- builder ----
FROM golang:1.23 AS builder
WORKDIR /src

# Prime module cache
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest and build static binary
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/bot ./

# ---- runner ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && adduser -D -g '' appuser
USER appuser
WORKDIR /app
COPY --from=builder /out/bot /app/bot

EXPOSE 8080
ENTRYPOINT ["/app/bot"]
# Flags (e.g., -live -interval 1) are provided by docker-compose
