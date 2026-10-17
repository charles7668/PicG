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

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 picg \
    && useradd --system --uid 10001 --gid picg --home-dir /app --no-create-home picg \
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
    CMD curl --fail --silent http://127.0.0.1:8080/health || exit 1

USER picg

ENTRYPOINT ["/usr/local/bin/picg"]
