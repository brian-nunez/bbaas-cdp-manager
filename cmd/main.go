package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brian-nunez/bbaas-cdp-manager/internal/browser"
	v1 "github.com/brian-nunez/bbaas-cdp-manager/internal/handlers/v1"
	"github.com/brian-nunez/bbaas-cdp-manager/internal/httpserver"
	"github.com/brian-nunez/bbaas-cdp-manager/internal/recorder"
)

func main() {
	cdpBindHost := envOrDefault("CDP_BIND_HOST", "127.0.0.1")
	cdpPublicHost := envOrDefault("CDP_PUBLIC_HOST", cdpBindHost)
	cdpPortMin := intEnvOrDefault("CDP_PORT_MIN", 20000)
	cdpPortMax := intEnvOrDefault("CDP_PORT_MAX", 29999)

	manager := browser.NewManager(browser.ManagerConfig{
		DefaultIdleTimeout: durationEnvOrDefault("BROWSER_IDLE_TIMEOUT", time.Minute),
		CleanupInterval:    durationEnvOrDefault("BROWSER_CLEANUP_INTERVAL", 5*time.Second),
		WorkerConcurrency:  intEnvOrDefault("TASK_WORKER_CONCURRENCY", 4),
		TaskLogPath:        envOrDefault("TASK_LOG_PATH", "./logs"),
		TaskDatabasePath:   envOrDefault("TASK_DB_PATH", "./tasks.db"),
		CDPBindHost:        cdpBindHost,
		CDPPublicHost:      cdpPublicHost,
		CDPPortMin:         cdpPortMin,
		CDPPortMax:         cdpPortMax,
		Headless:           boolEnvOrDefault("PLAYWRIGHT_HEADLESS", true),
	})

	if err := manager.Start(); err != nil {
		log.Fatalf("could not start browser manager: %v", err)
	}

	recorderStore, err := recorder.OpenStore(envOrDefault("RECORDINGS_DB_PATH", "./recordings.db"))
	if err != nil {
		_ = manager.Stop()
		log.Fatalf("could not open recorder store: %v", err)
	}

	recorderService := recorder.NewService(recorderStore, manager)

	server := httpserver.Bootstrap(httpserver.BootstrapConfig{
		V1Dependencies: v1.Dependencies{
			BrowserManager: manager,
			Recorder:       recorderService,
		},
	})

	PORT := os.Getenv("PORT")
	if PORT == "" {
		PORT = "8080"
	}

	go func() {
		err := server.Start(fmt.Sprintf("0.0.0.0:%s", PORT))
		if err != nil && err.Error() != "http: Server closed" {
			log.Fatalf("could not start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Println("Shutting down server...")
	err = server.Shutdown(ctx)
	if err != nil {
		log.Fatalf("server shutdown failed: %v", err)
	}

	if err := manager.Stop(); err != nil {
		log.Printf("browser manager shutdown completed with errors: %v", err)
	}

	if err := recorderService.Close(); err != nil {
		log.Printf("recorder shutdown completed with errors: %v", err)
	}

	log.Println("Server exited cleanly")
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	return value
}

func boolEnvOrDefault(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Printf("invalid %s value %q, using %t", key, value, fallback)
		return fallback
	}

	return parsed
}

func intEnvOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("invalid %s value %q, using %d", key, value, fallback)
		return fallback
	}

	return parsed
}

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		log.Printf("invalid %s value %q, using %s", key, value, fallback)
		return fallback
	}

	return parsed
}
