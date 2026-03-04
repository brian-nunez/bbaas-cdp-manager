package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/bbaas-cdp-manager/internal/recorder/replay"
)

func main() {
	cdpURL := mustEnv("REPLAY_CDP_URL")
	exportPath := mustEnv("REPLAY_EXPORT_PATH")

	delay := time.Duration(intEnvOrDefault("REPLAY_DELAY_MS", 120)) * time.Millisecond
	timeout := time.Duration(intEnvOrDefault("REPLAY_TIMEOUT_SECONDS", 120)) * time.Second

	exported, err := loadExport(exportPath)
	if err != nil {
		log.Fatalf("load export: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := replay.ReplayExport(ctx, cdpURL, exported, replay.Options{ActionDelay: delay})
	if err != nil {
		log.Fatalf("replay failed: %v", err)
	}

	out := map[string]interface{}{
		"recordingId":     exported.Recording.ID,
		"totalActions":    len(exported.Actions),
		"executedActions": result.ExecutedActions,
		"failedActions":   result.FailedActions,
		"errors":          result.Errors,
	}

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		log.Fatalf("encode replay summary: %v", err)
	}

	fmt.Println(string(encoded))
}

func loadExport(path string) (replay.RecordingExport, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return replay.RecordingExport{}, err
	}

	var exported replay.RecordingExport
	if err := json.Unmarshal(bytes, &exported); err != nil {
		return replay.RecordingExport{}, err
	}

	if strings.TrimSpace(exported.Recording.ID) == "" {
		return replay.RecordingExport{}, fmt.Errorf("recording.id is required")
	}

	return exported, nil
}

func mustEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		log.Fatalf("missing required env var: %s", key)
	}
	return value
}

func intEnvOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}

	return parsed
}
