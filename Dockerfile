# ---- build stage ----
FROM golang:1.23 AS builder
WORKDIR /src

# Speed up caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest
COPY . .

# Build static binary
ARG CGO_ENABLED=0
ARG GOOS=linux
ARG GOARCH=amd64
RUN CGO_ENABLED=${CGO_ENABLED} GOOS=${GOOS} GOARCH=${GOARCH} \
    go build -ldflags="-s -w" -o /out/bot .

# ---- runtime stage ----
# use distroless static (tiny) – no shell inside
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/bot /app/bot
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/bot"]
CMD ["-live","-interval","1"]
