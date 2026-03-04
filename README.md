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

### Recording Endpoints

Browser-scoped routes (`/api/v1/browsers/:id/recordings`):

- `POST /browsers/:id/recordings` starts recording for the browser.
- `POST /browsers/:id/recordings/:recordingId/stop` stops an active recording.
- `GET /browsers/:id/recordings` lists recordings for the browser.
- `GET /browsers/:id/recordings/:recordingId` returns one recording metadata entry.
- `GET /browsers/:id/recordings/:recordingId/actions?limit=200&offset=0` paginates actions.
- `GET /browsers/:id/recordings/:recordingId/export` returns one export JSON payload.

Recording-id routes (`/api/v1/recordings/:recordingId`):

- `GET /recordings/:recordingId` returns recording metadata.
- `POST /recordings/:recordingId/stop` stops the recording by ID.
- `GET /recordings/:recordingId/actions?limit=200&offset=0` paginates actions by recording ID.
- `GET /recordings/:recordingId/export` exports by recording ID.
- `POST /recordings/:recordingId/replay` replays into a target browser:
  - body: `{ "browserId": "brw_target", "actionDelayMs": 120, "timeoutSeconds": 120 }`

Example:

```bash
curl -X POST "http://localhost:8080/api/v1/browsers/brw_123/recordings" \
  -H "Content-Type: application/json" \
  -d '{"redactionMode":"maskInputs","includeScroll":true,"includeKeys":true}'
```

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
- `CDP_PORT_MIN` (default `20000`)
- `CDP_PORT_MAX` (default `29999`)
- `TASK_WORKER_CONCURRENCY` (default `4`)
- `TASK_DB_PATH` (default `./tasks.db`)
- `TASK_LOG_PATH` (default `./logs`)
- `RECORDINGS_DB_PATH` (default `./recordings.db`)

## CDP Networking Notes

- Chromium CDP is launched on loopback (`127.0.0.1`) by default for reliability.
- CDP ports are allocated from a dedicated range (`CDP_PORT_MIN`-`CDP_PORT_MAX`) to avoid collisions with OS ephemeral outbound ports.
- The service still returns externally-usable CDP URLs by rewriting host to `CDP_PUBLIC_HOST` while preserving each browser's dynamic port.
- For public access without exposing high ports, use an HTTP path translator proxy (for example `/<port>/... -> 127.0.0.1:<port>/...`) on the BBAAS machine.
