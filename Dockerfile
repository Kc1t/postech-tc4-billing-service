# ── Build ─────────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

# docs/ (Swagger) já vem gerado no repositório (make swagger).
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/billing-service ./cmd/api

# ── Runtime ───────────────────────────────────────────────────────────────────
FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata \
 && addgroup -S app -g 10001 \
 && adduser -S app -u 10001 -G app
ENV TZ=America/Sao_Paulo \
    APP_PORT=8082
WORKDIR /app
COPY --from=builder --chown=app:app /out/billing-service /app/billing-service

# Usuário sem privilégios (o Deployment também força runAsNonRoot).
USER 10001:10001
EXPOSE 8082
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8082/health/live >/dev/null || exit 1
ENTRYPOINT ["/app/billing-service"]
