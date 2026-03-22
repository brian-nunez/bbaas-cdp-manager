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
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

const (
	defaultIdleTimeout     = time.Minute
	defaultCleanupInterval = 5 * time.Second
	defaultWorkerCount     = 4
	defaultTaskLogPath     = "./logs"
	defaultCDPBindHost     = "127.0.0.1"
	defaultCDPPortMin      = 20000
	defaultCDPPortMax      = 29999
)

var (
	ErrBrowserNotFound   = errors.New("browser not found")
	ErrCDPPortOutOfRange = errors.New("cdp port out of range")
	ErrManagerNotStarted = errors.New("browser manager not started")
)

type ManagerConfig struct {
	DefaultIdleTimeout time.Duration
	CleanupInterval    time.Duration
	WorkerConcurrency  int
	TaskLogPath        string
	DatabaseURL        string
	CDPBindHost        string
	CDPPublicHost      string
	CDPPortMin         int
	CDPPortMax         int
	Headless           bool
}

type Manager struct {
	config ManagerConfig

	mu       sync.RWMutex
	sessions map[string]*session
	started  bool

	pw   *playwright.Playwright
	pool *taskWorkerPool
	db   *postgresSessionStore

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

	if strings.TrimSpace(c.DatabaseURL) == "" {
		c.DatabaseURL = defaultDatabaseURL
	}

	if strings.TrimSpace(c.CDPBindHost) == "" {
		c.CDPBindHost = defaultCDPBindHost
	}

	if strings.TrimSpace(c.CDPPublicHost) == "" {
		c.CDPPublicHost = c.CDPBindHost
	}

	if c.CDPPortMin <= 0 {
		c.CDPPortMin = defaultCDPPortMin
	}

	if c.CDPPortMax <= 0 {
		c.CDPPortMax = defaultCDPPortMax
	}

	if c.CDPPortMin > c.CDPPortMax {
		c.CDPPortMin = defaultCDPPortMin
		c.CDPPortMax = defaultCDPPortMax
	}

	return c
}

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	store, err := newPostgresSessionStore(context.Background(), m.config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("start postgres session store: %w", err)
	}

	if err := store.markAllActiveClosed(context.Background(), closeReasonManagerStart, time.Now().UTC()); err != nil {
		_ = store.close()
		return fmt.Errorf("reset stale active sessions: %w", err)
	}

	if err := store.markInFlightTasksFailed(context.Background(), "manager restarted before task completed", time.Now().UTC()); err != nil {
		_ = store.close()
		return fmt.Errorf("reset stale tasks: %w", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		_ = store.close()
		return fmt.Errorf("start playwright runtime: %w", err)
	}

	pool := newTaskWorkerPool(taskWorkerPoolConfig{
		Concurrency: m.config.WorkerConcurrency,
		LogPath:     m.config.TaskLogPath,
		Store:       store,
	})

	if err := pool.Start(); err != nil {
		_ = pw.Stop()
		_ = store.close()
		return fmt.Errorf("start task worker pool: %w", err)
	}

	m.pw = pw
	m.pool = pool
	m.db = store
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
		db := m.db

		sessions := make([]*session, 0, len(m.sessions))
		for _, s := range m.sessions {
			sessions = append(sessions, s)
		}
		m.sessions = make(map[string]*session)
		m.started = false
		m.db = nil

		m.mu.Unlock()

		if pool != nil {
			pool.Stop()
		}

		for _, s := range sessions {
			if err := s.browser.Close(); err != nil {
				combinedErr = errors.Join(combinedErr, fmt.Errorf("close browser %s: %w", s.id, err))
			}
		}

		if db != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := db.markAllActiveClosed(ctx, closeReasonManagerStop, time.Now().UTC()); err != nil {
				combinedErr = errors.Join(combinedErr, fmt.Errorf("mark sessions closed during shutdown: %w", err))
			}
			cancel()

			if err := db.close(); err != nil {
				combinedErr = errors.Join(combinedErr, fmt.Errorf("close postgres session store: %w", err))
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

	taskInfo, err := m.pool.AddTask(task, browserID, taskKindSpawnBrowser)
	if err != nil {
		return nil, fmt.Errorf("enqueue spawn task: %w", err)
	}

	if err := m.db.insertTaskEvent(ctx, taskEvent{
		BrowserID: browserID,
		ProcessID: taskInfo.ProcessID,
		WorkerID:  taskInfo.WorkerID,
		EventType: taskEventSpawnRequested,
	}); err != nil {
		log.Printf("failed to persist spawn_requested event for browser %s: %v", browserID, err)
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

	if err := m.db.touchActiveSession(context.Background(), browserID, now); err != nil {
		log.Printf("failed to persist keepalive for browser %s: %v", browserID, err)
	}

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

	taskInfo, err := m.pool.AddTask(task, browserID, taskKindCloseBrowser)
	if err != nil {
		return nil, fmt.Errorf("enqueue close task: %w", err)
	}

	if err := m.db.insertTaskEvent(ctx, taskEvent{
		BrowserID: browserID,
		ProcessID: taskInfo.ProcessID,
		WorkerID:  taskInfo.WorkerID,
		EventType: taskEventCloseRequested,
		Details: map[string]any{
			"closedByIdleSweep": closedByIdleSweep,
		},
	}); err != nil {
		log.Printf("failed to persist close_requested event for browser %s: %v", browserID, err)
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

func (m *Manager) closeBrowserNow(browserID string, reason closeReason) error {
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

	if err := m.db.closeActiveSession(context.Background(), browserID, reason, time.Now().UTC()); err != nil && !errors.Is(err, errSessionNotFound) {
		log.Printf("failed to persist close of browser %s: %v", browserID, err)
	}

	return nil
}

func (m *Manager) registerDisconnect(browserID string, b playwright.Browser) {
	b.OnDisconnected(func(playwright.Browser) {
		m.mu.Lock()
		delete(m.sessions, browserID)
		m.mu.Unlock()

		if err := m.db.closeActiveSession(context.Background(), browserID, closeReasonDisconnect, time.Now().UTC()); err != nil && !errors.Is(err, errSessionNotFound) {
			log.Printf("failed to persist disconnected browser %s: %v", browserID, err)
		}
	})
}

func (m *Manager) ensureStarted() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.started || m.pw == nil || m.pool == nil || m.db == nil {
		return ErrManagerNotStarted
	}

	return nil
}

func (m *Manager) ProxyCDP(w http.ResponseWriter, r *http.Request, port int, requestPath string) error {
	if err := m.ensureStarted(); err != nil {
		return err
	}

	if port < m.config.CDPPortMin || port > m.config.CDPPortMax {
		return ErrCDPPortOutOfRange
	}

	targetHost := cdpProbeHost(normalizeCDPBindHost(m.config.CDPBindHost))
	targetURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(targetHost, strconv.Itoa(port)),
	}

	targetPath := "/" + strings.TrimPrefix(requestPath, "/")
	if strings.TrimSpace(targetPath) == "/" {
		targetPath = "/"
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.URL.Path = targetPath
		req.URL.RawPath = targetPath
		req.Host = targetURL.Host
	}
	proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("cdp reverse proxy error for port %d: %v", port, err)
	}

	proxy.ServeHTTP(w, r)

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

func (t *spawnBrowserTask) Process(ctx context.Context, pc *taskProcessContext) error {
	_ = pc.Logger(fmt.Sprintf("spawning browser %s", t.browserID))

	recordFailure := func(taskErr error) {
		if taskErr == nil {
			return
		}

		if recordErr := t.manager.db.insertTaskEvent(context.Background(), taskEvent{
			BrowserID: t.browserID,
			ProcessID: pc.ProcessID,
			WorkerID:  &pc.WorkerID,
			EventType: taskEventSpawnFailed,
			Details: map[string]any{
				"error": taskErr.Error(),
			},
		}); recordErr != nil {
			log.Printf("failed to persist spawn_failed event for browser %s: %v", t.browserID, recordErr)
		}
	}

	select {
	case <-ctx.Done():
		err := ctx.Err()
		recordFailure(err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	default:
	}

	bindHost := normalizeCDPBindHost(t.manager.config.CDPBindHost)

	port, err := reserveOpenPortInRange(bindHost, t.manager.config.CDPPortMin, t.manager.config.CDPPortMax)
	if err != nil {
		err = fmt.Errorf("reserve open port: %w", err)
		recordFailure(err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	launchOptions := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(t.headless),
		Args: []string{
			"--no-sandbox",
			fmt.Sprintf("--remote-debugging-address=%s", bindHost),
			"--no-first-run",
			"--no-default-browser-check",
			"--remote-allow-origins=*",
			fmt.Sprintf("--remote-debugging-port=%d", port),
		},
	}

	browser, err := t.manager.pw.Chromium.Launch(launchOptions)
	if err != nil {
		err = fmt.Errorf("launch chromium: %w", err)
		recordFailure(err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	localCDPHTTPURL := fmt.Sprintf("http://%s:%d", cdpProbeHost(bindHost), port)

	publicHost := strings.TrimSpace(t.manager.config.CDPPublicHost)
	if publicHost == "" {
		publicHost = cdpProbeHost(bindHost)
	}

	publicCDPHTTPURL := fmt.Sprintf(
		"http://%s:%d",
		publicHost,
		port,
	)
	cdpWSURL, err := waitForWebSocketDebuggerURL(ctx, localCDPHTTPURL, 6*time.Second)
	if err != nil {
		_ = browser.Close()
		err = fmt.Errorf("discover cdp websocket url: %w", err)
		recordFailure(err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	cdpWSURL = rewriteEndpointHost(
		cdpWSURL,
		publicHost,
		port,
	)

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

	info := sessionToInfo(s)
	if err := t.manager.db.upsertActiveSession(ctx, info); err != nil {
		_ = browser.Close()
		t.manager.mu.Lock()
		delete(t.manager.sessions, t.browserID)
		t.manager.mu.Unlock()
		err = fmt.Errorf("persist browser session: %w", err)
		recordFailure(err)
		t.finish(spawnBrowserTaskResult{err: err})
		return err
	}

	if recordErr := t.manager.db.insertTaskEvent(context.Background(), taskEvent{
		BrowserID: t.browserID,
		ProcessID: pc.ProcessID,
		WorkerID:  &pc.WorkerID,
		EventType: taskEventSpawnSucceeded,
	}); recordErr != nil {
		log.Printf("failed to persist spawn_succeeded event for browser %s: %v", t.browserID, recordErr)
	}

	t.manager.registerDisconnect(t.browserID, browser)

	result := spawnBrowserTaskResult{info: info}
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

func (t *closeBrowserTask) Process(_ context.Context, pc *taskProcessContext) error {
	_ = pc.Logger(fmt.Sprintf("closing browser %s", t.browserID))

	reason := closeReasonManual
	if t.closedByIdleSweep {
		reason = closeReasonIdleSweep
	}

	err := t.manager.closeBrowserNow(t.browserID, reason)

	if err != nil {
		if recordErr := t.manager.db.insertTaskEvent(context.Background(), taskEvent{
			BrowserID: t.browserID,
			ProcessID: pc.ProcessID,
			WorkerID:  &pc.WorkerID,
			EventType: taskEventCloseFailed,
			Details: map[string]any{
				"error":             err.Error(),
				"closedByIdleSweep": t.closedByIdleSweep,
			},
		}); recordErr != nil {
			log.Printf("failed to persist close_failed event for browser %s: %v", t.browserID, recordErr)
		}

		if errors.Is(err, ErrBrowserNotFound) && t.closedByIdleSweep {
			t.finish(nil)
			return nil
		}
		t.finish(err)
		return err
	}

	if recordErr := t.manager.db.insertTaskEvent(context.Background(), taskEvent{
		BrowserID: t.browserID,
		ProcessID: pc.ProcessID,
		WorkerID:  &pc.WorkerID,
		EventType: taskEventCloseSucceeded,
		Details: map[string]any{
			"closedByIdleSweep": t.closedByIdleSweep,
		},
	}); recordErr != nil {
		log.Printf("failed to persist close_succeeded event for browser %s: %v", t.browserID, recordErr)
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

func reserveOpenPortInRange(host string, portMin int, portMax int) (int, error) {
	if portMin <= 0 || portMax <= 0 || portMin > portMax {
		return 0, fmt.Errorf("invalid port range %d-%d", portMin, portMax)
	}

	for port := portMin; port <= portMax; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}

	return 0, fmt.Errorf("no open port available in range %d-%d", portMin, portMax)
}

func normalizeCDPBindHost(bindHost string) string {
	switch strings.TrimSpace(bindHost) {
	case "", "0.0.0.0", "::":
		// Chromium CDP reliably binds to loopback in containerized environments.
		// Keep loopback as the practical default and rely on reverse proxies for external access.
		return "127.0.0.1"
	default:
		return bindHost
	}
}

func cdpProbeHost(bindHost string) string {
	switch strings.TrimSpace(bindHost) {
	case "", "0.0.0.0", "::", "localhost":
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
