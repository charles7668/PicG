FROM mcr.microsoft.com/devcontainers/go:1.25-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*

RUN GOPATH=/tmp/picg-tool-gopath GOMODCACHE=/tmp/picg-tool-modcache GOCACHE=/tmp/picg-tool-buildcache GOBIN=/usr/local/bin \
        go install github.com/a-h/templ/cmd/templ@v0.3.1020 \
    && GOPATH=/tmp/picg-tool-gopath GOMODCACHE=/tmp/picg-tool-modcache GOCACHE=/tmp/picg-tool-buildcache GOBIN=/usr/local/bin \
        go install golang.org/x/tools/gopls@v0.21.1 \
    && templ version \
    && gopls version \
    && rm -rf /tmp/picg-tool-gopath /tmp/picg-tool-modcache /tmp/picg-tool-buildcache
