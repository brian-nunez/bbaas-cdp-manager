DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'browser_session_status') THEN
        CREATE TYPE browser_session_status AS ENUM ('active', 'closed');
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'browser_close_reason') THEN
        CREATE TYPE browser_close_reason AS ENUM (
            'manual',
            'idle_sweep',
            'disconnect',
            'manager_stop',
            'manager_startup'
        );
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'browser_task_event_type') THEN
        CREATE TYPE browser_task_event_type AS ENUM (
            'spawn_requested',
            'spawn_succeeded',
            'spawn_failed',
            'close_requested',
            'close_succeeded',
            'close_failed'
        );
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'browser_task_kind') THEN
        CREATE TYPE browser_task_kind AS ENUM (
            'spawn_browser',
            'close_browser'
        );
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'browser_task_status') THEN
        CREATE TYPE browser_task_status AS ENUM (
            'pending',
            'running',
            'completed',
            'failed'
        );
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS browser_sessions (
    id TEXT PRIMARY KEY,
    status browser_session_status NOT NULL DEFAULT 'active',
    cdp_url TEXT NOT NULL,
    cdp_http_url TEXT NOT NULL,
    headless BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_active_at TIMESTAMPTZ NOT NULL,
    idle_timeout_seconds INTEGER NOT NULL CHECK (idle_timeout_seconds > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    close_reason browser_close_reason,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (
            status = 'active'::browser_session_status
            AND closed_at IS NULL
            AND close_reason IS NULL
        )
        OR (
            status = 'closed'::browser_session_status
            AND closed_at IS NOT NULL
            AND close_reason IS NOT NULL
        )
    )
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'browser_sessions'
          AND column_name = 'expires_at'
          AND is_generated = 'ALWAYS'
    ) THEN
        ALTER TABLE browser_sessions DROP COLUMN expires_at;
        ALTER TABLE browser_sessions ADD COLUMN expires_at TIMESTAMPTZ;
    END IF;
END
$$;

UPDATE browser_sessions
SET expires_at = last_active_at + (idle_timeout_seconds * INTERVAL '1 second')
WHERE expires_at IS NULL;

ALTER TABLE browser_sessions
ALTER COLUMN expires_at SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_browser_sessions_active_expires_at
    ON browser_sessions (expires_at)
    WHERE status = 'active'::browser_session_status;

CREATE INDEX IF NOT EXISTS idx_browser_sessions_status_updated_at
    ON browser_sessions (status, updated_at DESC);

CREATE TABLE IF NOT EXISTS browser_tasks (
    process_id TEXT PRIMARY KEY,
    browser_id TEXT NOT NULL,
    task_kind browser_task_kind NOT NULL,
    status browser_task_status NOT NULL DEFAULT 'pending',
    log_path TEXT NOT NULL,
    log_data TEXT NOT NULL DEFAULT '',
    worker_id INTEGER,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_browser_tasks_browser_created
    ON browser_tasks (browser_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_browser_tasks_status_updated
    ON browser_tasks (status, updated_at DESC);

CREATE TABLE IF NOT EXISTS browser_task_events (
    id BIGSERIAL PRIMARY KEY,
    browser_id TEXT NOT NULL,
    process_id TEXT NOT NULL,
    worker_id INTEGER,
    event_type browser_task_event_type NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_browser_task_events_browser_created
    ON browser_task_events (browser_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_browser_task_events_process
    ON browser_task_events (process_id);
