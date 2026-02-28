package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	worker "github.com/brian-nunez/task-orchestration"
	"github.com/playwright-community/playwright-go"
)

const (
	defaultIdleTimeout     = time.Minute
	defaultCleanupInterval = 5 * time.Second
	defaultWorkerCount     = 4
	defaultTaskLogPath     = "./logs"
	defaultTaskDBPath      = "./tasks.db"
	defaultCDPBindHost     = "127.0.0.1"
)

var (
	ErrBrowserNotFound   = errors.New("browser not found")
	ErrManagerNotStarted = errors.New("browser manager not started")
)

type ManagerConfig struct {
	DefaultIdleTimeout time.Duration
	CleanupInterval    time.Duration
	WorkerConcurrency  int
	TaskLogPath        string
	TaskDatabasePath   string
	CDPBindHost        string
	CDPPublicHost      string
	Headless           bool
}

type Manager struct {
	config ManagerConfig

	mu       sync.RWMutex
	sessions map[string]*session
	started  bool

	pw   *playwright.Playwright
	pool *worker.WorkerPool

	stopCh   chan struct{}
	stopOnce sync.Once
}

type session struct {
	id           string
	browser      playwright.Browser
	cdpURL       string
	cdpHTTPURL   string
	createdAt    time.Time
	lastActiveAt time.Time
	idleTimeout  time.Duration
	headless     bool
}

type BrowserInfo struct {
	ID                 string    `json:"id"`
	CDPURL             string    `json:"cdpUrl"`
	CDPHTTPURL         string    `json:"cdpHttpUrl"`
	Headless           bool      `json:"headless"`
	CreatedAt          time.Time `json:"createdAt"`
	LastActiveAt       time.Time `json:"lastActiveAt"`
	IdleTimeoutSeconds int64     `json:"idleTimeoutSeconds"`
	ExpiresAt          time.Time `json:"expiresAt"`
}

type CreateBrowserParams struct {
	Headless    *bool
	IdleTimeout time.Duration
}

type CreateBrowserResult struct {
	Browser           BrowserInfo `json:"browser"`
	SpawnTaskProcess  string      `json:"spawnTaskProcessId"`
	SpawnedByWorkerID *int        `json:"spawnedByWorkerId,omitempty"`
}

type CloseBrowserResult struct {
	BrowserID         string `json:"browserId"`
	CloseTaskProcess  string `json:"closeTaskProcessId"`
	ClosedByIdleSweep bool   `json:"closedByIdleSweep"`
}

func NewManager(config ManagerConfig) *Manager {
	config = config.withDefaults()

	return &Manager{
		config:   config,
		sessions: make(map[string]*session),
		stopCh:   make(chan struct{}),
	}
}

func (c ManagerConfig) withDefaults() ManagerConfig {
	if c.DefaultIdleTimeout <= 0 {
		c.DefaultIdleTimeout = defaultIdleTimeout
	}

	if c.CleanupInterval <= 0 {
		c.CleanupInterval = defaultCleanupInterval
	}

	if c.WorkerConcurrency <= 0 {
		c.WorkerConcurrency = defaultWorkerCount
	}

	if strings.TrimSpace(c.TaskLogPath) == "" {
		c.TaskLogPath = defaultTaskLogPath
	}

	if strings.TrimSpace(c.TaskDatabasePath) == "" {
		c.TaskDatabasePath = defaultTaskDBPath
	}

	if strings.TrimSpace(c.CDPBindHost) == "" {
		c.CDPBindHost = defaultCDPBindHost
	}

	if strings.TrimSpace(c.CDPPublicHost) == "" {
		c.CDPPublicHost = c.CDPBindHost
	}

	return c
}

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright runtime: %w", err)
	}

	pool := worker.NewWorkerPool(worker.WorkerPoolConfig{
		Concurrency:  m.config.WorkerConcurrency,
		LogPath:      m.config.TaskLogPath,
		DatabasePath: m.config.TaskDatabasePath,
	})

	if err := pool.Start(); err != nil {
		_ = pw.Stop()
		return fmt.Errorf("start task worker pool: %w", err)
	}

	m.pw = pw
	m.pool = pool
	m.started = true

	go m.cleanupLoop()

	return nil
}

func (m *Manager) Stop() error {
	var combinedErr error

	m.stopOnce.Do(func() {
		m.mu.Lock()

		if !m.started {
			m.mu.Unlock()
			return
		}

		close(m.stopCh)

		pool := m.pool
		pw := m.pw

		sessions := make([]*session, 0, len(m.sessions))
		for _, s := range m.sessions {
			sessions = append(sessions, s)
		}
		m.sessions = make(map[string]*session)
		m.started = false

		m.mu.Unlock()

		if pool != nil {
			pool.Stop()
		}

		for _, s := range sessions {
			if err := s.browser.Close(); err != nil {
				combinedErr = errors.Join(combinedErr, fmt.Errorf("close browser %s: %w", s.id, err))
			}
		}

		if pw != nil {
			if err := pw.Stop(); err != nil {
				combinedErr = errors.Join(combinedErr, fmt.Errorf("stop playwright runtime: %w", err))
			}
		}
	})

	return combinedErr
}

func (m *Manager) CreateBrowser(ctx context.Context, params CreateBrowserParams) (*CreateBrowserResult, error) {
	if err := m.ensureStarted(); err != nil {
		return nil, err
	}

	browserID, err := randomID("brw")
	if err != nil {
		return nil, fmt.Errorf("generate browser id: %w", err)
	}

	idleTimeout := m.config.DefaultIdleTimeout
	if params.IdleTimeout > 0 {
		idleTimeout = params.IdleTimeout
	}

	headless := m.config.Headless
	if params.Headless != nil {
		headless = *params.Headless
	}

	task := &spawnBrowserTask{
		manager:     m,
		browserID:   browserID,
		headless:    headless,
		idleTimeout: idleTimeout,
		resultCh:    make(chan spawnBrowserTaskResult, 1),
	}

	taskInfo, err := m.pool.AddTask(task)
	if err != nil {
		return nil, fmt.Errorf("enqueue spawn task: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-task.resultCh:
		if result.err != nil {
			return nil, result.err
		}

		return &CreateBrowserResult{
			Browser:           result.info,
			SpawnTaskProcess:  taskInfo.ProcessID,
			SpawnedByWorkerID: taskInfo.WorkerID,
		}, nil
	}
}

func (m *Manager) ListBrowsers() ([]BrowserInfo, error) {
	if err := m.ensureStarted(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]BrowserInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, sessionToInfo(s))
	}

	return out, nil
}

func (m *Manager) GetBrowser(browserID string) (*BrowserInfo, error) {
	if err := m.ensureStarted(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[browserID]
	if !ok {
		return nil, ErrBrowserNotFound
	}

	info := sessionToInfo(s)
	return &info, nil
}

func (m *Manager) KeepAlive(browserID string) (*BrowserInfo, error) {
	if err := m.ensureStarted(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[browserID]
	if !ok {
		return nil, ErrBrowserNotFound
	}

	s.lastActiveAt = now
	info := sessionToInfo(s)

	return &info, nil
}

func (m *Manager) CloseBrowser(ctx context.Context, browserID string, closedByIdleSweep bool) (*CloseBrowserResult, error) {
	if err := m.ensureStarted(); err != nil {
		return nil, err
	}

	task := &closeBrowserTask{
		manager:           m,
		browserID:         browserID,
		closedByIdleSweep: closedByIdleSweep,
		resultCh:          make(chan error, 1),
	}

	taskInfo, err := m.pool.AddTask(task)
	if err != nil {
		return nil, fmt.Errorf("enqueue close task: %w", err)
	}

	if !closedByIdleSweep {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case closeErr := <-task.resultCh:
			if closeErr != nil {
				return nil, closeErr
			}
		}
	}

	return &CloseBrowserResult{
		BrowserID:         browserID,
		CloseTaskProcess:  taskInfo.ProcessID,
		ClosedByIdleSweep: closedByIdleSweep,
	}, nil
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(m.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.closeIdleBrowsers()
		}
	}
}

func (m *Manager) closeIdleBrowsers() {
	if err := m.ensureStarted(); err != nil {
		return
	}

	now := time.Now().UTC()
	toClose := make([]string, 0)

	m.mu.RLock()
	for id, s := range m.sessions {
		if now.After(s.lastActiveAt.Add(s.idleTimeout)) {
			toClose = append(toClose, id)
		}
	}
	m.mu.RUnlock()

	for _, browserID := range toClose {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := m.CloseBrowser(ctx, browserID, true)
		cancel()
		if err != nil && !errors.Is(err, ErrBrowserNotFound) {
			log.Printf("failed to close idle browser %s: %v", browserID, err)
		}
	}
}

func (m *Manager) closeBrowserNow(browserID string) error {
	m.mu.Lock()
	s, ok := m.sessions[browserID]
	if ok {
		delete(m.sessions, browserID)
	}
	m.mu.Unlock()

	if !ok {
		return ErrBrowserNotFound
	}

	if err := s.browser.Close(); err != nil {
		return fmt.Errorf("close browser %s: %w", browserID, err)
	}

	return nil
}

func (m *Manager) registerDisconnect(browserID string, b playwright.Browser) {
	b.OnDisconnected(func(playwright.Browser) {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.sessions, browserID)
	})
}

func (m *Manager) ensureStarted() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.started || m.pw == nil || m.pool == nil {
		return ErrManagerNotStarted
	}

	return nil
}

func sessionToInfo(s *session) BrowserInfo {
	return BrowserInfo{
		ID:                 s.id,
		CDPURL:             s.cdpURL,
		CDPHTTPURL:         s.cdpHTTPURL,
		Headless:           s.headless,
		CreatedAt:          s.createdAt,
		LastActiveAt:       s.lastActiveAt,
		IdleTimeoutSeconds: int64(s.idleTimeout.Seconds()),
		ExpiresAt:          s.lastActiveAt.Add(s.idleTimeout),
	}
}

type spawnBrowserTask struct {
	manager     *Manager
	browserID   string
	headless    bool
	idleTimeout time.Duration
	resultCh    chan spawnBrowserTaskResult
}

type spawnBrowserTaskResult struct {
	info BrowserInfo
	err  error
}

func (t *spawnBrowserTask) Process(ctx context.Context, pc *worker.ProcessContext) error {
	_ = pc.Logger(fmt.Sprintf("spawning browser %s", t.browserID))

	select {
	case <-ctx.Done():
		err := ctx.Err()
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	default:
	}

	port, err := reserveOpenPort(t.manager.config.CDPBindHost)
	if err != nil {
		err = fmt.Errorf("reserve open port: %w", err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	launchOptions := playwright.BrowserTypeLaunchOptions{
		Headless:          playwright.Bool(t.headless),
		IgnoreDefaultArgs: []string{"--remote-debugging-pipe"},
		Args: []string{
			"--remote-debugging-address=0.0.0.0",
			fmt.Sprintf("--remote-debugging-port=%d", port),
		},
	}

	browser, err := t.manager.pw.Chromium.Launch(launchOptions)
	if err != nil {
		err = fmt.Errorf("launch chromium: %w", err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	localCDPHost := discoveryHost(t.manager.config.CDPBindHost)
	localCDPHTTPURL := fmt.Sprintf("http://%s", net.JoinHostPort(localCDPHost, strconv.Itoa(port)))
	publicCDPHTTPURL := fmt.Sprintf("http://%s", net.JoinHostPort(t.manager.config.CDPPublicHost, strconv.Itoa(port)))

	cdpWSURL, err := waitForWebSocketDebuggerURL(ctx, localCDPHTTPURL, 6*time.Second)
	if err != nil {
		_ = browser.Close()
		err = fmt.Errorf("discover cdp websocket url: %w", err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	cdpWSURL = rewriteEndpointHost(cdpWSURL, t.manager.config.CDPPublicHost, port)

	now := time.Now().UTC()
	s := &session{
		id:           t.browserID,
		browser:      browser,
		cdpURL:       cdpWSURL,
		cdpHTTPURL:   publicCDPHTTPURL,
		createdAt:    now,
		lastActiveAt: now,
		idleTimeout:  t.idleTimeout,
		headless:     t.headless,
	}

	t.manager.mu.Lock()
	t.manager.sessions[t.browserID] = s
	t.manager.mu.Unlock()
	t.manager.registerDisconnect(t.browserID, browser)

	result := spawnBrowserTaskResult{info: sessionToInfo(s)}
	t.finish(result)

	return nil
}

func (t *spawnBrowserTask) finish(result spawnBrowserTaskResult) {
	select {
	case t.resultCh <- result:
	default:
	}
}

type closeBrowserTask struct {
	manager           *Manager
	browserID         string
	closedByIdleSweep bool
	resultCh          chan error
}

func (t *closeBrowserTask) Process(_ context.Context, pc *worker.ProcessContext) error {
	_ = pc.Logger(fmt.Sprintf("closing browser %s", t.browserID))

	err := t.manager.closeBrowserNow(t.browserID)

	if err != nil {
		if errors.Is(err, ErrBrowserNotFound) && t.closedByIdleSweep {
			t.finish(nil)
			return nil
		}
		t.finish(err)
		return err
	}

	t.finish(nil)
	return nil
}

func (t *closeBrowserTask) finish(err error) {
	if t.resultCh == nil {
		return
	}

	select {
	case t.resultCh <- err:
	default:
	}
}

func reserveOpenPort(host string) (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("listener did not return tcp addr")
	}

	return addr.Port, nil
}

func discoveryHost(bindHost string) string {
	switch strings.TrimSpace(bindHost) {
	case "", "0.0.0.0", "::":
		return "127.0.0.1"
	default:
		return bindHost
	}
}

func waitForWebSocketDebuggerURL(ctx context.Context, cdpHTTPURL string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout: 1200 * time.Millisecond,
	}

	var lastErr error

	for {
		endpoint := strings.TrimSuffix(cdpHTTPURL, "/") + "/json/version"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			wsURL, parseErr := parseWebSocketDebuggerURL(resp.Body)
			_ = resp.Body.Close()
			if parseErr == nil && wsURL != "" {
				return wsURL, nil
			}
			lastErr = parseErr
		}

		select {
		case <-ctx.Done():
			if lastErr == nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("%w: %v", ctx.Err(), lastErr)
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func parseWebSocketDebuggerURL(body io.Reader) (string, error) {
	payload := struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}{}

	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return "", err
	}

	if strings.TrimSpace(payload.WebSocketDebuggerURL) == "" {
		return "", errors.New("webSocketDebuggerUrl not found")
	}

	return payload.WebSocketDebuggerURL, nil
}

func rewriteEndpointHost(rawURL, host string, port int) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	parsed.Host = net.JoinHostPort(host, strconv.Itoa(port))

	return parsed.String()
}

func randomID(prefix string) (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(bytes)), nil
}
