# Build PicG with the Go toolchain, then ship only the binary and FFmpeg.
FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/picg \
    .

FROM alpine:3.24 AS runtime

RUN apk add --no-cache ca-certificates ffmpeg \
    && addgroup -S -g 10001 picg \
    && adduser -S -D -H -u 10001 -G picg -h /app picg \
    && mkdir -p /app \
    && chown picg:picg /app

WORKDIR /app

COPY --from=builder --chown=picg:picg /out/picg /usr/local/bin/picg

ENV PORT=8080 \
    PIC_DIR=/app/pic \
    PICG_CONFIG=/app/settings.json \
    PICG_DATABASE=/app/app.sqlite \
    PICG_THUMBNAILS=/app/thumbnails

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1

USER picg

ENTRYPOINT ["/usr/local/bin/picg"]
