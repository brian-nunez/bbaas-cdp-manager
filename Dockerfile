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
ENV TASK_DB_PATH=/data/tasks.db
ENV TASK_LOG_PATH=/data/logs

# CDP behavior:
# - Chromium will still bind its CDP port to 127.0.0.1 inside the container
# - socat will expose it externally via 0.0.0.0
ENV CDP_BIND_HOST=127.0.0.1
ENV CDP_PUBLIC_HOST=localhost

# How much to offset the externally-exposed port from the internal CDP port.
# Example: if chrome uses 34535, socat listens on 44535.
ENV CDP_PORT_OFFSET=10000

ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
ENV PLAYWRIGHT_DRIVER_PATH=/ms-playwright-go

RUN apt-get update && apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  git \
  socat \
  procps \
  iproute2 \
  && rm -rf /var/lib/apt/lists/*

# Install Playwright driver + Chromium browser during build.
COPY --from=builder /usr/local/go /usr/local/go
RUN mkdir -p /ms-playwright /ms-playwright-go /data/logs /app \
  && PATH="/usr/local/go/bin:${PATH}" go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5200.1 install --with-deps chromium \
  && rm -rf /usr/local/go /root/go /root/.cache/go-build

COPY --from=builder /out/bbaas-cdp /usr/local/bin/bbaas-cdp

# Entrypoint wrapper: starts your app, and (optionally) socat forwarders for any CDP ports it opens.
# This assumes your app starts browsers whose CDP ports appear as LISTEN on 127.0.0.1:<port> by "headless_shell" or "chrome".
# It forwards each discovered CDP port to 0.0.0.0:<port+CDP_PORT_OFFSET>.
RUN cat > /usr/local/bin/entrypoint.sh <<'EOF'
#!/bin/sh
set -eu

OFFSET="${CDP_PORT_OFFSET:-10000}"

# Start the app
/usr/local/bin/bbaas-cdp &
APP_PID="$!"

# Background loop: discover CDP listeners and create socat forwarders.
# Forwarders are idempotent: if already running for a port, skip.
(
  while kill -0 "$APP_PID" 2>/dev/null; do
    # Find loopback CDP listeners owned by chrome/headless_shell
    for p in $(ss -ltnp 2>/dev/null \
        | awk '/127\.0\.0\.1:[0-9]+/ && ($0 ~ /headless_shell|chrome/) {print $4}' \
        | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p' \
        | sort -n | uniq); do

      public=$((p + OFFSET))

      # If socat already listening on public port, skip
      if ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ":${public}\$"; then
        continue
      fi

      # Start a forwarder: 0.0.0.0:public -> 127.0.0.1:p
      # -T: idle timeout (seconds) to reap dead connections
      # fork: allow multiple clients
      socat -T 60 TCP-LISTEN:${public},fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:${p} >/dev/null 2>&1 &
    done

    sleep 0.25
  done
) &

# Propagate signals and exit code
trap 'kill -TERM "$APP_PID" 2>/dev/null || true; wait "$APP_PID" || true' INT TERM
wait "$APP_PID"
EOF
RUN chmod +x /usr/local/bin/entrypoint.sh

WORKDIR /app

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
