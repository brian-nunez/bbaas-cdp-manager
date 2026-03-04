package recorder

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsOrderedActions(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "recordings.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	recording := Recording{
		ID:        "rec_test_1",
		BrowserID: "brw_1",
		Status:    recordingStatusRecording,
		CreatedAt: now,
		StartedAt: now,
		Meta:      map[string]interface{}{"key": "value"},
	}

	if err := store.CreateRecording(context.Background(), recording); err != nil {
		t.Fatalf("create recording: %v", err)
	}

	actions := []Action{
		{ID: "act_2", RecordingID: recording.ID, TSMonotonicMS: 10, TSWallISO: now.Format(time.RFC3339Nano), Type: "click", Payload: map[string]interface{}{}, OrderIndex: 1},
		{ID: "act_1", RecordingID: recording.ID, TSMonotonicMS: 5, TSWallISO: now.Format(time.RFC3339Nano), Type: "focus", Payload: map[string]interface{}{}, OrderIndex: 0},
	}

	for _, action := range actions {
		if err := store.InsertAction(context.Background(), action); err != nil {
			t.Fatalf("insert action: %v", err)
		}
	}

	stored, err := store.ListAllActions(context.Background(), recording.ID)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(stored))
	}

	if stored[0].OrderIndex != 0 || stored[1].OrderIndex != 1 {
		t.Fatalf("actions not ordered by order_index: %+v", stored)
	}
}
