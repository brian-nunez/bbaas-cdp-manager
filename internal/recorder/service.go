package recorder

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brian-nunez/bbaas-cdp-manager/internal/browser"
	replaypkg "github.com/brian-nunez/bbaas-cdp-manager/internal/recorder/replay"
	"github.com/playwright-community/playwright-go"
)

const (
	recordingStatusRecording = "recording"
	recordingStatusStopped   = "stopped"
	defaultEventBufferSize   = 2048
	recorderScanInterval     = 1200 * time.Millisecond
)

type BrowserManager interface {
	GetBrowser(browserID string) (*browser.BrowserInfo, error)
	RegisterBrowserClosedListener(listener func(browser.BrowserClosedEvent))
}

type Service struct {
	store   *Store
	manager BrowserManager

	mu                sync.Mutex
	activeByRecording map[string]*activeRecording
	activeByBrowser   map[string]string
}

type activeRecording struct {
	recordingID string
	browserID   string
	pw          *playwright.Playwright
	browser     playwright.Browser
	options     StartRecordingOptions

	eventsCh    chan capturedAction
	writerStop  chan struct{}
	writerDone  chan struct{}
	scannerStop chan struct{}
	scannerDone chan struct{}

	orderIndex atomic.Int64
	stopOnce   sync.Once

	pageSessions   map[string]playwright.CDPSession
	pageSessionsMu sync.Mutex
}

type capturedAction struct {
	ActionType    string
	TSMonotonicMS float64
	TSWallISO     string
	FrameURL      string
	Payload       map[string]interface{}
}

func NewService(store *Store, manager BrowserManager) *Service {
	service := &Service{
		store:             store,
		manager:           manager,
		activeByRecording: make(map[string]*activeRecording),
		activeByBrowser:   make(map[string]string),
	}

	if manager != nil {
		manager.RegisterBrowserClosedListener(service.onBrowserClosed)
	}

	return service
}

func (s *Service) StartRecording(ctx context.Context, browserID string, options StartRecordingOptions) (Recording, error) {
	if s.store == nil || s.manager == nil {
		return Recording{}, fmt.Errorf("recorder dependencies are not configured")
	}

	browserID = strings.TrimSpace(browserID)
	if browserID == "" {
		return Recording{}, browser.ErrBrowserNotFound
	}

	options = options.WithDefaults()
	if !options.IsValid() {
		return Recording{}, ErrInvalidOptions
	}

	browserInfo, err := s.manager.GetBrowser(browserID)
	if err != nil {
		return Recording{}, err
	}

	s.mu.Lock()
	if _, exists := s.activeByBrowser[browserID]; exists {
		s.mu.Unlock()
		return Recording{}, ErrRecordingAlreadyOpen
	}
	s.mu.Unlock()

	hasActive, _, err := s.store.HasActiveRecordingForBrowser(ctx, browserID)
	if err != nil {
		return Recording{}, err
	}
	if hasActive {
		return Recording{}, ErrRecordingAlreadyOpen
	}

	recordingID, err := randomID("rec")
	if err != nil {
		return Recording{}, fmt.Errorf("generate recording id: %w", err)
	}

	now := time.Now().UTC()
	recording := Recording{
		ID:        recordingID,
		BrowserID: browserID,
		Status:    recordingStatusRecording,
		CreatedAt: now,
		StartedAt: now,
		Meta: map[string]interface{}{
			"cdpUrl":             browserInfo.CDPURL,
			"cdpHttpUrl":         browserInfo.CDPHTTPURL,
			"headless":           browserInfo.Headless,
			"idleTimeoutSeconds": browserInfo.IdleTimeoutSeconds,
			"redactionMode":      options.RedactionMode,
			"includeScroll":      options.IncludeScroll != nil && *options.IncludeScroll,
			"includeKeys":        options.IncludeKeys != nil && *options.IncludeKeys,
		},
	}

	if err := s.store.CreateRecording(ctx, recording); err != nil {
		return Recording{}, err
	}

	pw, err := playwright.Run()
	if err != nil {
		return Recording{}, fmt.Errorf("start recorder playwright runtime: %w", err)
	}

	cdpEndpoint := strings.TrimSpace(browserInfo.CDPHTTPURL)
	if cdpEndpoint == "" {
		cdpEndpoint = strings.TrimSpace(browserInfo.CDPURL)
	}

	recorderBrowser, err := pw.Chromium.ConnectOverCDP(cdpEndpoint)
	if err != nil {
		_ = pw.Stop()
		return Recording{}, fmt.Errorf("connect recorder to browser CDP: %w", err)
	}

	active := &activeRecording{
		recordingID:  recordingID,
		browserID:    browserID,
		pw:           pw,
		browser:      recorderBrowser,
		options:      options,
		eventsCh:     make(chan capturedAction, defaultEventBufferSize),
		writerStop:   make(chan struct{}),
		writerDone:   make(chan struct{}),
		scannerStop:  make(chan struct{}),
		scannerDone:  make(chan struct{}),
		pageSessions: make(map[string]playwright.CDPSession),
	}

	s.mu.Lock()
	s.activeByRecording[recordingID] = active
	s.activeByBrowser[browserID] = recordingID
	s.mu.Unlock()

	go s.runWriter(active)
	go s.runScanner(active)

	// Instrument already-open pages immediately and continue scanning for new ones.
	if err := s.instrumentOpenPages(active); err != nil {
		log.Printf("recorder: initial page instrumentation failed for %s: %v", recordingID, err)
	}

	return recording, nil
}

func (s *Service) StopRecording(ctx context.Context, browserID string, recordingID string, stopReason string) (StopRecordingResult, error) {
	browserID = strings.TrimSpace(browserID)
	recordingID = strings.TrimSpace(recordingID)
	if browserID == "" || recordingID == "" {
		return StopRecordingResult{}, ErrRecordingNotFound
	}

	if _, err := s.store.GetRecordingByBrowser(ctx, browserID, recordingID); err != nil {
		return StopRecordingResult{}, err
	}

	if active := s.getActiveRecording(recordingID); active != nil {
		s.stopActiveRecording(active, stopReason)
	}

	recording, err := s.store.GetRecordingByBrowser(ctx, browserID, recordingID)
	if err != nil {
		return StopRecordingResult{}, err
	}

	return StopRecordingResult{
		Recording:   recording,
		ActionCount: recording.ActionCount,
	}, nil
}

func (s *Service) StopRecordingByID(ctx context.Context, recordingID string, stopReason string) (StopRecordingResult, error) {
	recordingID = strings.TrimSpace(recordingID)
	if recordingID == "" {
		return StopRecordingResult{}, ErrRecordingNotFound
	}

	recording, err := s.store.GetRecording(ctx, recordingID)
	if err != nil {
		return StopRecordingResult{}, err
	}

	if active := s.getActiveRecording(recordingID); active != nil {
		s.stopActiveRecording(active, stopReason)
	}

	recording, err = s.store.GetRecording(ctx, recordingID)
	if err != nil {
		return StopRecordingResult{}, err
	}

	return StopRecordingResult{
		Recording:   recording,
		ActionCount: recording.ActionCount,
	}, nil
}

func (s *Service) ListRecordings(ctx context.Context, browserID string) ([]Recording, error) {
	browserID = strings.TrimSpace(browserID)
	if browserID == "" {
		return nil, browser.ErrBrowserNotFound
	}

	return s.store.ListRecordingsByBrowser(ctx, browserID)
}

func (s *Service) GetRecording(ctx context.Context, browserID string, recordingID string) (Recording, error) {
	browserID = strings.TrimSpace(browserID)
	recordingID = strings.TrimSpace(recordingID)
	if browserID == "" || recordingID == "" {
		return Recording{}, ErrRecordingNotFound
	}

	return s.store.GetRecordingByBrowser(ctx, browserID, recordingID)
}

func (s *Service) GetRecordingByID(ctx context.Context, recordingID string) (Recording, error) {
	recordingID = strings.TrimSpace(recordingID)
	if recordingID == "" {
		return Recording{}, ErrRecordingNotFound
	}

	return s.store.GetRecording(ctx, recordingID)
}

func (s *Service) ListActions(ctx context.Context, browserID string, recordingID string, limit int, offset int) ([]Action, error) {
	if _, err := s.store.GetRecordingByBrowser(ctx, browserID, recordingID); err != nil {
		return nil, err
	}

	return s.store.ListActions(ctx, recordingID, limit, offset)
}

func (s *Service) ListActionsByID(ctx context.Context, recordingID string, limit int, offset int) ([]Action, error) {
	recordingID = strings.TrimSpace(recordingID)
	if recordingID == "" {
		return nil, ErrRecordingNotFound
	}

	if _, err := s.store.GetRecording(ctx, recordingID); err != nil {
		return nil, err
	}

	return s.store.ListActions(ctx, recordingID, limit, offset)
}

func (s *Service) ExportRecording(ctx context.Context, browserID string, recordingID string) (RecordingExport, error) {
	recording, err := s.store.GetRecordingByBrowser(ctx, browserID, recordingID)
	if err != nil {
		return RecordingExport{}, err
	}

	actions, err := s.store.ListAllActions(ctx, recordingID)
	if err != nil {
		return RecordingExport{}, err
	}

	return RecordingExport{
		Recording: recording,
		Actions:   actions,
	}, nil
}

func (s *Service) ExportRecordingByID(ctx context.Context, recordingID string) (RecordingExport, error) {
	recordingID = strings.TrimSpace(recordingID)
	if recordingID == "" {
		return RecordingExport{}, ErrRecordingNotFound
	}

	recording, err := s.store.GetRecording(ctx, recordingID)
	if err != nil {
		return RecordingExport{}, err
	}

	actions, err := s.store.ListAllActions(ctx, recordingID)
	if err != nil {
		return RecordingExport{}, err
	}

	return RecordingExport{
		Recording: recording,
		Actions:   actions,
	}, nil
}

func (s *Service) ReplayRecording(ctx context.Context, recordingID string, request ReplayRecordingRequest) (ReplayRecordingResult, error) {
	recordingID = strings.TrimSpace(recordingID)
	targetBrowserID := strings.TrimSpace(request.BrowserID)
	if recordingID == "" {
		return ReplayRecordingResult{}, ErrRecordingNotFound
	}
	if targetBrowserID == "" {
		return ReplayRecordingResult{}, ErrInvalidOptions
	}

	exported, err := s.ExportRecordingByID(ctx, recordingID)
	if err != nil {
		return ReplayRecordingResult{}, err
	}

	browserInfo, err := s.manager.GetBrowser(targetBrowserID)
	if err != nil {
		return ReplayRecordingResult{}, err
	}

	replayCtx := ctx
	if request.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		replayCtx, cancel = context.WithTimeout(ctx, time.Duration(request.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	delayMS := request.ActionDelayMS
	if delayMS < 0 {
		delayMS = 0
	}

	cdpURL := strings.TrimSpace(browserInfo.CDPURL)
	if cdpURL == "" {
		cdpURL = strings.TrimSpace(browserInfo.CDPHTTPURL)
	}
	if cdpURL == "" {
		return ReplayRecordingResult{}, fmt.Errorf("target browser has no cdp endpoint")
	}

	replayExport := replaypkg.RecordingExport{
		Recording: replaypkg.Recording{ID: exported.Recording.ID},
		Actions:   make([]replaypkg.Action, 0, len(exported.Actions)),
	}
	for _, action := range exported.Actions {
		replayExport.Actions = append(replayExport.Actions, replaypkg.Action{
			ID:         action.ID,
			Type:       action.Type,
			FrameURL:   action.FrameURL,
			Payload:    action.Payload,
			OrderIndex: action.OrderIndex,
		})
	}

	replayResult, err := replaypkg.ReplayExport(replayCtx, cdpURL, replayExport, replaypkg.Options{
		ActionDelay: time.Duration(delayMS) * time.Millisecond,
	})
	if err != nil {
		return ReplayRecordingResult{}, err
	}

	return ReplayRecordingResult{
		RecordingID:     recordingID,
		BrowserID:       targetBrowserID,
		ExecutedActions: replayResult.ExecutedActions,
		FailedActions:   replayResult.FailedActions,
		Errors:          replayResult.Errors,
	}, nil
}

func (s *Service) Close() error {
	active := s.listActiveRecordings()
	for _, recording := range active {
		s.stopActiveRecording(recording, "service_shutdown")
	}

	if s.store != nil {
		return s.store.Close()
	}

	return nil
}

func (s *Service) listActiveRecordings() []*activeRecording {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*activeRecording, 0, len(s.activeByRecording))
	for _, recording := range s.activeByRecording {
		out = append(out, recording)
	}

	return out
}

func (s *Service) getActiveRecording(recordingID string) *activeRecording {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.activeByRecording[recordingID]
}

func (s *Service) stopActiveRecording(active *activeRecording, stopReason string) {
	active.stopOnce.Do(func() {
		close(active.scannerStop)
		<-active.scannerDone

		sessions := s.detachAllSessions(active)
		for _, session := range sessions {
			_ = session.Detach()
		}

		if active.browser != nil {
			_ = active.browser.Close()
		}
		if active.pw != nil {
			_ = active.pw.Stop()
		}

		close(active.writerStop)
		<-active.writerDone

		now := time.Now().UTC()
		reason := strings.TrimSpace(stopReason)
		if reason == "" {
			reason = "stopped_by_user"
		}

		if err := s.store.StopRecording(context.Background(), active.recordingID, now, reason); err != nil && err != ErrRecordingNotFound {
			log.Printf("recorder: stop recording %s failed: %v", active.recordingID, err)
		}

		s.mu.Lock()
		delete(s.activeByRecording, active.recordingID)
		if existing, ok := s.activeByBrowser[active.browserID]; ok && existing == active.recordingID {
			delete(s.activeByBrowser, active.browserID)
		}
		s.mu.Unlock()
	})
}

func (s *Service) runWriter(active *activeRecording) {
	defer close(active.writerDone)

	for {
		select {
		case action := <-active.eventsCh:
			s.persistCapturedAction(active, action)
		case <-active.writerStop:
			for {
				select {
				case action := <-active.eventsCh:
					s.persistCapturedAction(active, action)
				default:
					return
				}
			}
		}
	}
}

func (s *Service) persistCapturedAction(active *activeRecording, action capturedAction) {
	if strings.TrimSpace(action.ActionType) == "" {
		return
	}

	actionID, err := randomID("act")
	if err != nil {
		log.Printf("recorder: generate action id failed: %v", err)
		return
	}

	if strings.TrimSpace(action.TSWallISO) == "" {
		action.TSWallISO = time.Now().UTC().Format(time.RFC3339Nano)
	}

	orderIndex := int(active.orderIndex.Add(1) - 1)

	err = s.store.InsertAction(context.Background(), Action{
		ID:            actionID,
		RecordingID:   active.recordingID,
		TSMonotonicMS: action.TSMonotonicMS,
		TSWallISO:     action.TSWallISO,
		Type:          action.ActionType,
		FrameURL:      action.FrameURL,
		Payload:       action.Payload,
		OrderIndex:    orderIndex,
	})
	if err != nil {
		log.Printf("recorder: persist action failed for %s: %v", active.recordingID, err)
	}
}

func (s *Service) runScanner(active *activeRecording) {
	defer close(active.scannerDone)

	ticker := time.NewTicker(recorderScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-active.scannerStop:
			return
		case <-ticker.C:
			if err := s.instrumentOpenPages(active); err != nil {
				log.Printf("recorder: scan instrumentation failed for %s: %v", active.recordingID, err)
			}
		}
	}
}

func (s *Service) instrumentOpenPages(active *activeRecording) error {
	contexts := active.browser.Contexts()
	for _, browserContext := range contexts {
		for _, page := range browserContext.Pages() {
			if err := s.instrumentPage(active, page); err != nil {
				log.Printf("recorder: instrument page failed for %s: %v", active.recordingID, err)
			}
		}
	}

	return nil
}

func (s *Service) instrumentPage(active *activeRecording, page playwright.Page) error {
	pageKey := fmt.Sprintf("%p", page)

	active.pageSessionsMu.Lock()
	if _, exists := active.pageSessions[pageKey]; exists {
		active.pageSessionsMu.Unlock()
		return nil
	}
	active.pageSessionsMu.Unlock()

	session, err := page.Context().NewCDPSession(page)
	if err != nil {
		return fmt.Errorf("create cdp session for page: %w", err)
	}

	session.On("Runtime.bindingCalled", func(params map[string]interface{}) {
		s.handleBindingCalled(active, params)
	})
	session.On("Page.frameNavigated", func(params map[string]interface{}) {
		s.handleFrameNavigated(active, params)
	})

	if _, err := session.Send("Runtime.enable", nil); err != nil {
		_ = session.Detach()
		return fmt.Errorf("enable runtime domain: %w", err)
	}

	if _, err := session.Send("Page.enable", nil); err != nil {
		_ = session.Detach()
		return fmt.Errorf("enable page domain: %w", err)
	}

	if _, err := session.Send("Runtime.addBinding", map[string]interface{}{"name": recorderBindingName}); err != nil {
		_ = session.Detach()
		return fmt.Errorf("register runtime binding: %w", err)
	}

	if _, err := session.Send("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{
		"source": buildRecorderScript(active.options),
	}); err != nil {
		_ = session.Detach()
		return fmt.Errorf("inject recorder script for new documents: %w", err)
	}

	_, _ = session.Send("Runtime.evaluate", map[string]interface{}{
		"expression":    "window.__bbaasInstallRecorder && window.__bbaasInstallRecorder();",
		"returnByValue": true,
	})

	active.pageSessionsMu.Lock()
	active.pageSessions[pageKey] = session
	active.pageSessionsMu.Unlock()

	return nil
}

func (s *Service) handleFrameNavigated(active *activeRecording, params map[string]interface{}) {
	frame, _ := params["frame"].(map[string]interface{})
	if frame == nil {
		return
	}

	url := strings.TrimSpace(anyToString(frame["url"]))
	if url == "" {
		return
	}

	action := capturedAction{
		ActionType:    "navigate",
		TSMonotonicMS: float64(time.Now().UTC().UnixMilli()),
		TSWallISO:     time.Now().UTC().Format(time.RFC3339Nano),
		FrameURL:      url,
		Payload: map[string]interface{}{
			"url":         url,
			"frameId":     anyToString(frame["id"]),
			"parentFrame": anyToString(frame["parentId"]),
			"name":        anyToString(frame["name"]),
		},
	}

	s.enqueueAction(active, action)
}

func (s *Service) handleBindingCalled(active *activeRecording, params map[string]interface{}) {
	name := strings.TrimSpace(anyToString(params["name"]))
	if name != recorderBindingName {
		return
	}

	rawPayload := anyToString(params["payload"])
	if strings.TrimSpace(rawPayload) == "" {
		return
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(rawPayload), &decoded); err != nil {
		log.Printf("recorder: invalid binding payload for %s: %v", active.recordingID, err)
		return
	}

	actionType := strings.TrimSpace(strings.ToLower(anyToString(decoded["type"])))
	if actionType == "" {
		return
	}

	captured := capturedAction{
		ActionType:    actionType,
		TSMonotonicMS: toFloat64(decoded["tsMonotonicMs"]),
		TSWallISO:     strings.TrimSpace(anyToString(decoded["tsWallIso"])),
		FrameURL:      strings.TrimSpace(anyToString(decoded["frameUrl"])),
		Payload:       mapFromAny(decoded["payload"]),
	}

	s.enqueueAction(active, captured)
}

func (s *Service) enqueueAction(active *activeRecording, action capturedAction) {
	select {
	case active.eventsCh <- action:
	default:
		log.Printf("recorder: action buffer full for %s, dropping %s", active.recordingID, action.ActionType)
	}
}

func (s *Service) detachAllSessions(active *activeRecording) []playwright.CDPSession {
	active.pageSessionsMu.Lock()
	defer active.pageSessionsMu.Unlock()

	sessions := make([]playwright.CDPSession, 0, len(active.pageSessions))
	for _, session := range active.pageSessions {
		sessions = append(sessions, session)
	}
	active.pageSessions = make(map[string]playwright.CDPSession)

	return sessions
}

func (s *Service) onBrowserClosed(event browser.BrowserClosedEvent) {
	if strings.TrimSpace(event.BrowserID) == "" {
		return
	}

	go func() {
		s.mu.Lock()
		recordingID, found := s.activeByBrowser[event.BrowserID]
		s.mu.Unlock()
		if !found {
			return
		}

		active := s.getActiveRecording(recordingID)
		if active == nil {
			return
		}

		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "browser_closed"
		} else {
			reason = "browser_" + reason
		}

		s.stopActiveRecording(active, reason)
	}()
}

func anyToString(input interface{}) string {
	if input == nil {
		return ""
	}

	switch value := input.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprintf("%v", input)
	}
}

func toFloat64(input interface{}) float64 {
	switch value := input.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil {
			return parsed
		}
	}

	return 0
}

func mapFromAny(input interface{}) map[string]interface{} {
	if input == nil {
		return map[string]interface{}{}
	}

	decoded, ok := input.(map[string]interface{})
	if !ok || decoded == nil {
		return map[string]interface{}{}
	}

	return decoded
}
