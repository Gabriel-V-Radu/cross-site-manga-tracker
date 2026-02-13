FROM golang:1.23-alpine AS builder
WORKDIR /app

COPY backend/go.mod backend/go.sum* ./backend/
WORKDIR /app/backend
RUN go mod download

COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /cross-site-tracker-api ./cmd/api

FROM alpine:3.21
WORKDIR /app
RUN adduser -D -g '' appuser

COPY --from=builder /cross-site-tracker-api /usr/local/bin/cross-site-tracker-api
COPY backend/migrations ./migrations
COPY backend/web ./web
COPY backend/connectors ./connectors

RUN mkdir -p /app/data && chown -R appuser:appuser /app
USER appuser

EXPOSE 8080
CMD ["cross-site-tracker-api"]
