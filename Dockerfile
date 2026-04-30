FROM golang:1.22-alpine AS builder

ARG VERSION=dev
WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.VERSION=${VERSION}" \
    -o iptv .

# ────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache tzdata ca-certificates

WORKDIR /app
COPY --from=builder /app/iptv .

ENV TZ=Asia/Shanghai
EXPOSE 5000

ENTRYPOINT ["/app/iptv"]
