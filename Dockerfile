FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY backend/go.mod backend/go.sum* ./backend/
WORKDIR /app/backend
RUN go mod download

COPY backend/ .

# The operational commands ship alongside the server: the deployment host has no
# Go toolchain, so `go run ./cmd/...` is not available there. Built in one layer
# so they share Go's package cache instead of recompiling the same deps.
# poll-once is deliberately absent: it is a development rehearsal tool that
# writes to whatever database it is pointed at.
RUN CGO_ENABLED=0 GOOS=linux go build -o /cross-site-tracker-api ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build -o /cleanup-stale-sources ./cmd/cleanup-stale-sources \
 && CGO_ENABLED=0 GOOS=linux go build -o /repair-latest-chapter ./cmd/repair-latest-chapter

FROM alpine:3.21
WORKDIR /app
RUN adduser -D -g '' appuser

COPY --from=builder /cross-site-tracker-api /usr/local/bin/cross-site-tracker-api
COPY --from=builder /cleanup-stale-sources /usr/local/bin/cleanup-stale-sources
COPY --from=builder /repair-latest-chapter /usr/local/bin/repair-latest-chapter
COPY backend/web ./web

RUN mkdir -p /app/data && chown -R appuser:appuser /app
USER appuser

EXPOSE 8080
CMD ["cross-site-tracker-api"]
