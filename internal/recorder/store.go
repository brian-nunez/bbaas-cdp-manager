package recorder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultRecordingsDBPath = "./recordings.db"

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = defaultRecordingsDBPath
	}

	dsn := trimmed
	if !strings.HasPrefix(trimmed, "file:") {
		dsn = fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", filepath.Clean(trimmed))
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open recorder sqlite db: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping recorder sqlite db: %w", err)
	}

	store := &Store{db: db}
	if err := store.runMigrations(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}

	return s.db.Close()
}

func (s *Store) runMigrations(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS recordings (
			id TEXT PRIMARY KEY,
			browser_id TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			started_at TIMESTAMP NOT NULL,
			stopped_at TIMESTAMP,
			stop_reason TEXT,
			meta_json TEXT NOT NULL DEFAULT '{}',
			CHECK(status IN ('recording', 'stopped'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_recordings_browser_id ON recordings(browser_id)`,
		`CREATE INDEX IF NOT EXISTS idx_recordings_status ON recordings(status)`,
		`CREATE TABLE IF NOT EXISTS actions (
			id TEXT PRIMARY KEY,
			recording_id TEXT NOT NULL,
			ts_monotonic_ms REAL,
			ts_wall_iso TEXT NOT NULL,
			type TEXT NOT NULL,
			frame_url TEXT,
			payload_json TEXT NOT NULL,
			order_index INTEGER NOT NULL,
			FOREIGN KEY(recording_id) REFERENCES recordings(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_actions_recording_order ON actions(recording_id, order_index)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_actions_recording_order_unique ON actions(recording_id, order_index)`,
	}

	for idx, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("run recorder migration %d: %w", idx+1, err)
		}
	}

	return nil
}

func (s *Store) CreateRecording(ctx context.Context, recording Recording) error {
	metaJSON, err := marshalJSONObject(recording.Meta)
	if err != nil {
		return fmt.Errorf("marshal recording meta: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO recordings (id, browser_id, status, created_at, started_at, stopped_at, stop_reason, meta_json)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		recording.ID,
		recording.BrowserID,
		recording.Status,
		recording.CreatedAt,
		recording.StartedAt,
		recording.StoppedAt,
		nullableString(recording.StopReason),
		metaJSON,
	)
	if err != nil {
		return fmt.Errorf("insert recording: %w", err)
	}

	return nil
}

func (s *Store) StopRecording(ctx context.Context, recordingID string, stoppedAt time.Time, stopReason string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE recordings
		 SET status = 'stopped',
			 stopped_at = $1,
			 stop_reason = $2
		 WHERE id = $3`,
		stoppedAt,
		nullableString(stopReason),
		recordingID,
	)
	if err != nil {
		return fmt.Errorf("update recording status: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("recording stop rows affected: %w", err)
	}
	if affected == 0 {
		return ErrRecordingNotFound
	}

	return nil
}

func (s *Store) GetRecording(ctx context.Context, recordingID string) (Recording, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, browser_id, status, created_at, started_at, stopped_at, stop_reason, meta_json
		 FROM recordings
		 WHERE id = $1`,
		recordingID,
	)

	recording, err := scanRecording(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Recording{}, ErrRecordingNotFound
		}
		return Recording{}, fmt.Errorf("get recording: %w", err)
	}

	actionCount, err := s.CountActions(ctx, recording.ID)
	if err != nil {
		return Recording{}, err
	}
	recording.ActionCount = actionCount

	return recording, nil
}

func (s *Store) GetRecordingByBrowser(ctx context.Context, browserID string, recordingID string) (Recording, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, browser_id, status, created_at, started_at, stopped_at, stop_reason, meta_json
		 FROM recordings
		 WHERE id = $1 AND browser_id = $2`,
		recordingID,
		browserID,
	)

	recording, err := scanRecording(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Recording{}, ErrRecordingNotFound
		}
		return Recording{}, fmt.Errorf("get recording by browser: %w", err)
	}

	actionCount, err := s.CountActions(ctx, recording.ID)
	if err != nil {
		return Recording{}, err
	}
	recording.ActionCount = actionCount

	return recording, nil
}

func (s *Store) ListRecordingsByBrowser(ctx context.Context, browserID string) ([]Recording, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, browser_id, status, created_at, started_at, stopped_at, stop_reason, meta_json
		 FROM recordings
		 WHERE browser_id = $1
		 ORDER BY created_at DESC`,
		browserID,
	)
	if err != nil {
		return nil, fmt.Errorf("list recordings by browser: %w", err)
	}
	defer rows.Close()

	out := make([]Recording, 0)
	for rows.Next() {
		recording, scanErr := scanRecording(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan recording: %w", scanErr)
		}

		actionCount, countErr := s.CountActions(ctx, recording.ID)
		if countErr != nil {
			return nil, countErr
		}
		recording.ActionCount = actionCount

		out = append(out, recording)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recordings: %w", err)
	}

	return out, nil
}

func (s *Store) HasActiveRecordingForBrowser(ctx context.Context, browserID string) (bool, string, error) {
	var recordingID string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id
		 FROM recordings
		 WHERE browser_id = $1 AND status = 'recording'
		 ORDER BY created_at DESC
		 LIMIT 1`,
		browserID,
	).Scan(&recordingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("query active recording: %w", err)
	}

	return true, recordingID, nil
}

func (s *Store) InsertAction(ctx context.Context, action Action) error {
	payloadJSON, err := marshalJSONObject(action.Payload)
	if err != nil {
		return fmt.Errorf("marshal action payload: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO actions (id, recording_id, ts_monotonic_ms, ts_wall_iso, type, frame_url, payload_json, order_index)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		action.ID,
		action.RecordingID,
		action.TSMonotonicMS,
		action.TSWallISO,
		action.Type,
		nullableString(action.FrameURL),
		payloadJSON,
		action.OrderIndex,
	)
	if err != nil {
		return fmt.Errorf("insert action: %w", err)
	}

	return nil
}

func (s *Store) CountActions(ctx context.Context, recordingID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*)
		 FROM actions
		 WHERE recording_id = $1`,
		recordingID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count recording actions: %w", err)
	}

	return count, nil
}

func (s *Store) ListActions(ctx context.Context, recordingID string, limit int, offset int) ([]Action, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, recording_id, ts_monotonic_ms, ts_wall_iso, type, frame_url, payload_json, order_index
		 FROM actions
		 WHERE recording_id = $1
		 ORDER BY order_index ASC
		 LIMIT $2 OFFSET $3`,
		recordingID,
		limit,
		offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	defer rows.Close()

	actions := make([]Action, 0, limit)
	for rows.Next() {
		action, scanErr := scanAction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		actions = append(actions, action)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate actions: %w", err)
	}

	return actions, nil
}

func (s *Store) ListAllActions(ctx context.Context, recordingID string) ([]Action, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, recording_id, ts_monotonic_ms, ts_wall_iso, type, frame_url, payload_json, order_index
		 FROM actions
		 WHERE recording_id = $1
		 ORDER BY order_index ASC`,
		recordingID,
	)
	if err != nil {
		return nil, fmt.Errorf("list all actions: %w", err)
	}
	defer rows.Close()

	actions := make([]Action, 0)
	for rows.Next() {
		action, scanErr := scanAction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		actions = append(actions, action)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all actions: %w", err)
	}

	return actions, nil
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanRecording(s rowScanner) (Recording, error) {
	var recording Recording
	var stoppedAt sql.NullTime
	var stopReason sql.NullString
	var metaJSON string

	err := s.Scan(
		&recording.ID,
		&recording.BrowserID,
		&recording.Status,
		&recording.CreatedAt,
		&recording.StartedAt,
		&stoppedAt,
		&stopReason,
		&metaJSON,
	)
	if err != nil {
		return Recording{}, err
	}

	if stoppedAt.Valid {
		recording.StoppedAt = &stoppedAt.Time
	}
	if stopReason.Valid {
		recording.StopReason = stopReason.String
	}

	meta, err := unmarshalJSONObject(metaJSON)
	if err != nil {
		return Recording{}, fmt.Errorf("decode recording meta: %w", err)
	}
	recording.Meta = meta

	return recording, nil
}

func scanAction(s rowScanner) (Action, error) {
	var action Action
	var frameURL sql.NullString
	var payloadJSON string

	err := s.Scan(
		&action.ID,
		&action.RecordingID,
		&action.TSMonotonicMS,
		&action.TSWallISO,
		&action.Type,
		&frameURL,
		&payloadJSON,
		&action.OrderIndex,
	)
	if err != nil {
		return Action{}, fmt.Errorf("scan action: %w", err)
	}

	if frameURL.Valid {
		action.FrameURL = frameURL.String
	}

	payload, err := unmarshalJSONObject(payloadJSON)
	if err != nil {
		return Action{}, fmt.Errorf("decode action payload: %w", err)
	}
	action.Payload = payload

	return action, nil
}

func marshalJSONObject(input map[string]interface{}) (string, error) {
	if input == nil {
		input = map[string]interface{}{}
	}

	bytes, err := json.Marshal(input)
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

func unmarshalJSONObject(raw string) (map[string]interface{}, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]interface{}{}, nil
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}

	if decoded == nil {
		decoded = map[string]interface{}{}
	}

	return decoded, nil
}

func nullableString(input string) interface{} {
	if strings.TrimSpace(input) == "" {
		return nil
	}

	return input
}
