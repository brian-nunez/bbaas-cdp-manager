package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errWorkerPoolStopped = errors.New("worker pool stopped")

type workerTask interface {
	Process(ctx context.Context, pc *taskProcessContext) error
}

type taskProcessContext struct {
	WorkerID  int
	ProcessID string
	LogPath   string

	mu     sync.Mutex
	writer io.Writer
}

func (pc *taskProcessContext) Logger(message string) error {
	if pc == nil || pc.writer == nil {
		return nil
	}

	trimmed := strings.TrimRight(message, "\n")
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), trimmed)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	_, err := io.WriteString(pc.writer, line)
	return err
}

type workerTaskInfo struct {
	ProcessID string
	WorkerID  *int
}

type queuedTask struct {
	task      workerTask
	processID string
	browserID string
	kind      taskKind
	logPath   string
}

type taskWorkerPoolConfig struct {
	Concurrency int
	LogPath     string
	Store       *postgresSessionStore
}

type taskWorkerPool struct {
	config taskWorkerPoolConfig

	tasksChan chan queuedTask
	wg        sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	stopped bool

	stopOnce sync.Once
}

func newTaskWorkerPool(config taskWorkerPoolConfig) *taskWorkerPool {
	if config.Concurrency <= 0 {
		config.Concurrency = defaultWorkerCount
	}

	if strings.TrimSpace(config.LogPath) == "" {
		config.LogPath = defaultTaskLogPath
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &taskWorkerPool{
		config:    config,
		tasksChan: make(chan queuedTask),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (p *taskWorkerPool) Start() error {
	if p == nil {
		return errors.New("worker pool is nil")
	}

	if p.config.Store == nil {
		return errors.New("worker pool requires a postgres store")
	}

	if err := os.MkdirAll(p.config.LogPath, 0o755); err != nil {
		return fmt.Errorf("create task log path: %w", err)
	}

	for i := 0; i < p.config.Concurrency; i++ {
		go p.workerLoop(i)
	}

	return nil
}

func (p *taskWorkerPool) Stop() {
	if p == nil {
		return
	}

	p.stopOnce.Do(func() {
		p.cancel()

		p.mu.Lock()
		p.stopped = true
		close(p.tasksChan)
		p.mu.Unlock()

		p.wg.Wait()
	})
}

func (p *taskWorkerPool) AddTask(task workerTask, browserID string, kind taskKind) (*workerTaskInfo, error) {
	if p == nil {
		return nil, errors.New("worker pool is nil")
	}
	if task == nil {
		return nil, errors.New("task is nil")
	}

	processID, err := randomID("tsk")
	if err != nil {
		return nil, fmt.Errorf("generate task process id: %w", err)
	}

	logPath := filepath.Join(p.config.LogPath, fmt.Sprintf("%s.log", processID))
	if err := p.config.Store.createTask(context.Background(), processID, browserID, kind, logPath); err != nil {
		return nil, fmt.Errorf("persist task creation: %w", err)
	}

	item := queuedTask{
		task:      task,
		processID: processID,
		browserID: browserID,
		kind:      kind,
		logPath:   logPath,
	}

	p.mu.RLock()
	if p.stopped {
		p.mu.RUnlock()
		return nil, errWorkerPoolStopped
	}
	p.wg.Add(1)
	p.tasksChan <- item
	p.mu.RUnlock()

	return &workerTaskInfo{ProcessID: processID}, nil
}

func (p *taskWorkerPool) workerLoop(workerID int) {
	for item := range p.tasksChan {
		p.executeTask(workerID, item)
	}
}

func (p *taskWorkerPool) executeTask(workerID int, item queuedTask) {
	defer p.wg.Done()

	now := time.Now().UTC()
	if err := p.config.Store.startTask(context.Background(), item.processID, workerID, now); err != nil {
		_ = p.config.Store.failTask(context.Background(), item.processID, workerID, err.Error(), "", time.Now().UTC())
		return
	}

	file, err := os.OpenFile(item.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		_ = p.config.Store.failTask(context.Background(), item.processID, workerID, err.Error(), "", time.Now().UTC())
		return
	}

	pc := &taskProcessContext{
		WorkerID:  workerID,
		ProcessID: item.processID,
		LogPath:   item.logPath,
		writer:    file,
	}

	_ = pc.Logger(fmt.Sprintf("starting %s task for browser %s", item.kind, item.browserID))
	taskErr := item.task.Process(p.ctx, pc)
	if taskErr != nil {
		_ = pc.Logger(fmt.Sprintf("task failed: %v", taskErr))
	} else {
		_ = pc.Logger("task completed")
	}

	_ = file.Close()

	logBytes, readErr := os.ReadFile(item.logPath)
	logData := string(logBytes)
	if readErr != nil {
		if logData != "" && !strings.HasSuffix(logData, "\n") {
			logData += "\n"
		}
		logData += fmt.Sprintf("failed to read task log file: %v", readErr)
	}

	finishedAt := time.Now().UTC()
	if taskErr != nil {
		_ = p.config.Store.failTask(context.Background(), item.processID, workerID, taskErr.Error(), logData, finishedAt)
		return
	}

	_ = p.config.Store.completeTask(context.Background(), item.processID, workerID, logData, finishedAt)
}
