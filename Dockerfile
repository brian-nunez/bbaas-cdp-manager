FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/bbaas-cdp ./cmd/main.go


FROM debian:bookworm-slim

ENV PORT=8080
ENV PLAYWRIGHT_HEADLESS=true
ENV BROWSER_IDLE_TIMEOUT=1m
ENV BROWSER_CLEANUP_INTERVAL=5s
ENV TASK_WORKER_CONCURRENCY=4
ENV DATABASE_URL=
ENV TASK_LOG_PATH=/data/logs

ENV CDP_BIND_HOST=127.0.0.1
ENV CDP_PUBLIC_HOST=localhost

ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
ENV PLAYWRIGHT_DRIVER_PATH=/ms-playwright-go

RUN apt-get update && apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  git \
  && rm -rf /var/lib/apt/lists/*

# Install Playwright driver + Chromium browser during build.
COPY --from=builder /usr/local/go /usr/local/go
RUN mkdir -p /ms-playwright /ms-playwright-go /data/logs /app \
  && PATH="/usr/local/go/bin:${PATH}" go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5200.1 install --with-deps chromium \
  && rm -rf /usr/local/go /root/go /root/.cache/go-build

COPY --from=builder /out/bbaas-cdp /usr/local/bin/bbaas-cdp

WORKDIR /app

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/bbaas-cdp"]
