# Build stage
FROM golang:1.26-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o wa-gateway -ldflags="-s -w" .

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/wa-gateway .
COPY --from=builder /app/icon.ico .

# Create storage directories
RUN mkdir -p storages/qrcode storages/senditems storages/media

EXPOSE 3000

ENV APP_HOST=0.0.0.0
ENV APP_PORT=3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:3000/health || exit 1

CMD ["./wa-gateway"]
