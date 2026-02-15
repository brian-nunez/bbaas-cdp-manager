# Browser CDP Manager (Go + Playwright)

This service is a thin browser lifecycle manager:

- Spins up Chromium instances with Playwright.
- Exposes CDP connection URLs per browser.
- Auto-closes idle browsers (default: 1 minute).
- Provides a small HTTP API for spawn/inspect/keepalive/close.
- Uses `github.com/brian-nunez/task-orchestration` for spawn/close task execution.

## API

Base path: `/api/v1`

### `POST /browsers`
Creates a browser session and returns CDP connection info.

Request body (optional):

```json
{
  "headless": true,
  "idleTimeoutSeconds": 60
}
```

Response:

```json
{
  "browser": {
    "id": "brw_abc123",
    "cdpUrl": "ws://127.0.0.1:54321/devtools/browser/...",
    "cdpHttpUrl": "http://127.0.0.1:54321",
    "headless": true,
    "createdAt": "2026-02-15T20:00:00Z",
    "lastActiveAt": "2026-02-15T20:00:00Z",
    "idleTimeoutSeconds": 60,
    "expiresAt": "2026-02-15T20:01:00Z"
  },
  "spawnTaskProcessId": "process-id",
  "spawnedByWorkerId": 1
}
```

### `GET /browsers`
Lists active browser sessions.

### `GET /browsers/:id`
Gets one active browser session.

### `POST /browsers/:id/keepalive`
Refreshes browser idle timer (extends expiration from now).

### `DELETE /browsers/:id`
Closes a browser session.

### `GET /health`
Basic health response.

## Idle Timeout Behavior

- Default idle timeout: `1m`.
- Cleanup loop checks for idle browsers every `5s` (default).
- A browser is considered active when created or when `keepalive` is called.
- If a browser is idle past `lastActiveAt + idleTimeout`, it is closed automatically.

## Environment Variables

- `PORT` (default `8080`)
- `BROWSER_IDLE_TIMEOUT` (default `1m`)
- `BROWSER_CLEANUP_INTERVAL` (default `5s`)
- `PLAYWRIGHT_HEADLESS` (default `true`)
- `CDP_BIND_HOST` (default `127.0.0.1`)
- `CDP_PUBLIC_HOST` (default `CDP_BIND_HOST`)
- `TASK_WORKER_CONCURRENCY` (default `4`)
- `TASK_DB_PATH` (default `./tasks.db`)
- `TASK_LOG_PATH` (default `./logs`)

