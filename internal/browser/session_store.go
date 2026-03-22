package browser

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultDatabaseURL = ""
)

type closeReason string

const (
	closeReasonManual       closeReason = "manual"
	closeReasonIdleSweep    closeReason = "idle_sweep"
	closeReasonDisconnect   closeReason = "disconnect"
	closeReasonManagerStop  closeReason = "manager_stop"
	closeReasonManagerStart closeReason = "manager_startup"
)

type taskEventType string

const (
	taskEventSpawnRequested taskEventType = "spawn_requested"
	taskEventSpawnSucceeded taskEventType = "spawn_succeeded"
	taskEventSpawnFailed    taskEventType = "spawn_failed"
	taskEventCloseRequested taskEventType = "close_requested"
	taskEventCloseSucceeded taskEventType = "close_succeeded"
	taskEventCloseFailed    taskEventType = "close_failed"
)

type taskKind string

const (
	taskKindSpawnBrowser taskKind = "spawn_browser"
	taskKindCloseBrowser taskKind = "close_browser"
)

var errSessionNotFound = errors.New("session not found")

//go:embed sql/postgres_schema.sql
var postgresSchema string

type postgresSessionStore struct {
	db *sql.DB
}

type taskEvent struct {
	BrowserID string
	ProcessID string
	WorkerID  *int
	EventType taskEventType
	Details   map[string]any
}

func newPostgresSessionStore(ctx context.Context, databaseURL string) (*postgresSessionStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}

	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetMaxIdleConns(4)
	db.SetMaxOpenConns(16)

	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	schemaCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(schemaCtx, postgresSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply postgres schema: %w", err)
	}

	return &postgresSessionStore{db: db}, nil
}

func (s *postgresSessionStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}

	return s.db.Close()
}

func (s *postgresSessionStore) markAllActiveClosed(ctx context.Context, reason closeReason, closedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE browser_sessions
		SET
			status = 'closed',
			closed_at = $1,
			close_reason = $2,
			updated_at = NOW()
		WHERE status = 'active'
	`, closedAt, string(reason))
	if err != nil {
		return fmt.Errorf("mark active sessions closed: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) markInFlightTasksFailed(ctx context.Context, failureMessage string, finishedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE browser_tasks
		SET
			status = 'failed',
			error = $1,
			finished_at = COALESCE(finished_at, $2),
			updated_at = NOW()
		WHERE status IN ('pending', 'running')
	`, failureMessage, finishedAt)
	if err != nil {
		return fmt.Errorf("mark in-flight tasks failed: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) upsertActiveSession(ctx context.Context, info BrowserInfo) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO browser_sessions (
			id,
			status,
			cdp_url,
			cdp_http_url,
			headless,
			created_at,
			last_active_at,
			idle_timeout_seconds,
			expires_at,
			metadata,
			updated_at
		)
		VALUES ($1, 'active', $2, $3, $4, $5, $6, $7, $8, '{}'::jsonb, NOW())
		ON CONFLICT (id)
		DO UPDATE SET
			status = 'active',
			cdp_url = EXCLUDED.cdp_url,
			cdp_http_url = EXCLUDED.cdp_http_url,
			headless = EXCLUDED.headless,
			created_at = EXCLUDED.created_at,
			last_active_at = EXCLUDED.last_active_at,
			idle_timeout_seconds = EXCLUDED.idle_timeout_seconds,
			expires_at = EXCLUDED.expires_at,
			closed_at = NULL,
			close_reason = NULL,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
	`, info.ID, info.CDPURL, info.CDPHTTPURL, info.Headless, info.CreatedAt, info.LastActiveAt, info.IdleTimeoutSeconds, info.ExpiresAt)
	if err != nil {
		return fmt.Errorf("upsert active session: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) touchActiveSession(ctx context.Context, browserID string, lastActiveAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE browser_sessions
		SET
			last_active_at = $2,
			expires_at = $2 + (idle_timeout_seconds * INTERVAL '1 second'),
			updated_at = NOW()
		WHERE id = $1
		AND status = 'active'
	`, browserID, lastActiveAt)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch session rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return errSessionNotFound
	}

	return nil
}

func (s *postgresSessionStore) closeActiveSession(ctx context.Context, browserID string, reason closeReason, closedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE browser_sessions
		SET
			status = 'closed',
			closed_at = $2,
			close_reason = $3,
			updated_at = NOW()
		WHERE id = $1
		AND status = 'active'
	`, browserID, closedAt, string(reason))
	if err != nil {
		return fmt.Errorf("close session: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("close session rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return errSessionNotFound
	}

	return nil
}

func (s *postgresSessionStore) insertTaskEvent(ctx context.Context, event taskEvent) error {
	if s == nil || s.db == nil {
		return nil
	}

	details := event.Details
	if details == nil {
		details = map[string]any{}
	}

	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal task event details: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO browser_task_events (
			browser_id,
			process_id,
			worker_id,
			event_type,
			details
		)
		VALUES ($1, $2, $3, $4, $5::jsonb)
	`, event.BrowserID, event.ProcessID, event.WorkerID, string(event.EventType), string(detailsJSON))
	if err != nil {
		return fmt.Errorf("insert task event: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) createTask(ctx context.Context, processID string, browserID string, kind taskKind, logPath string) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO browser_tasks (
			process_id,
			browser_id,
			task_kind,
			status,
			log_path,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, 'pending', $4, NOW(), NOW())
	`, processID, browserID, string(kind), logPath)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) startTask(ctx context.Context, processID string, workerID int, startedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE browser_tasks
		SET
			status = 'running',
			worker_id = $2,
			started_at = $3,
			updated_at = NOW()
		WHERE process_id = $1
	`, processID, workerID, startedAt)
	if err != nil {
		return fmt.Errorf("start task: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) completeTask(ctx context.Context, processID string, workerID int, logData string, finishedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE browser_tasks
		SET
			status = 'completed',
			worker_id = $2,
			log_data = $3,
			error = '',
			finished_at = $4,
			updated_at = NOW()
		WHERE process_id = $1
	`, processID, workerID, logData, finishedAt)
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}

	return nil
}

func (s *postgresSessionStore) failTask(ctx context.Context, processID string, workerID int, errMessage string, logData string, finishedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE browser_tasks
		SET
			status = 'failed',
			worker_id = $2,
			log_data = $3,
			error = $4,
			finished_at = $5,
			updated_at = NOW()
		WHERE process_id = $1
	`, processID, workerID, logData, errMessage, finishedAt)
	if err != nil {
		return fmt.Errorf("fail task: %w", err)
	}

	return nil
}
