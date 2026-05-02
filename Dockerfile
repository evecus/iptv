# ── Build stage ──────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

ARG VERSION=dev
WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod tidy 2>/dev/null; true

COPY . .
RUN go mod tidy && \
    CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.VERSION=${VERSION}" \
    -o iptv .

# ── Runtime stage ─────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache tzdata ca-certificates

WORKDIR /app
COPY --from=builder /app/iptv .

ENV TZ=Asia/Shanghai

# ── Runtime defaults ──────────────────────────────────────────────
# All can be overridden with -e in docker run, or env: in compose.
# CLI flags always take precedence over env vars.
ENV PORT=3030
ENV WORKERS=20
ENV TOP=5
ENV INTERVAL=6h
# ENV URL1=http://your-custom-subscribe.m3u
# ENV URL2=http://another-subscribe.m3u

EXPOSE 3030

ENTRYPOINT ["/app/iptv"]
CMD ["--port", "3030", "--workers", "20", "--top", "5", "--interval", "6h"]
