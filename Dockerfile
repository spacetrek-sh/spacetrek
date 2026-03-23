# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a statically-linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/server ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/seed ./cmd/seed

# ── Runtime stage ───────────────────────────────────────────────────────────────
FROM scratch

# Copy CA certificates for outbound TLS calls (e.g. LLM APIs)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

WORKDIR /app

COPY --from=builder /app/server /app/server
COPY --from=builder /app/seed /app/seed
COPY --from=builder /app/seeds /app/seeds

EXPOSE 8080

ENV HTTP_ADDR=:8080

ENTRYPOINT ["/app/server"]
