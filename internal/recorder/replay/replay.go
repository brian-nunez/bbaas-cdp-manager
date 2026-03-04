package replay

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

type Options struct {
	ActionDelay time.Duration
}

type Result struct {
	ExecutedActions int      `json:"executedActions"`
	FailedActions   int      `json:"failedActions"`
	Errors          []string `json:"errors,omitempty"`
}

type Recording struct {
	ID string `json:"id"`
}

type Action struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	FrameURL   string                 `json:"frameUrl"`
	Payload    map[string]interface{} `json:"payload"`
	OrderIndex int                    `json:"orderIndex"`
}

type RecordingExport struct {
	Recording Recording `json:"recording"`
	Actions   []Action  `json:"actions"`
}

type locator struct {
	Kind  string
	Value string
	Role  string
	Name  string
	Match string
}

func ReplayExport(ctx context.Context, cdpURL string, export RecordingExport, options Options) (Result, error) {
	if strings.TrimSpace(cdpURL) == "" {
		return Result{}, fmt.Errorf("cdp url is required")
	}

	pw, err := playwright.Run()
	if err != nil {
		return Result{}, fmt.Errorf("start playwright: %w", err)
	}
	defer func() {
		_ = pw.Stop()
	}()

	browser, err := pw.Chromium.ConnectOverCDP(cdpURL)
	if err != nil {
		return Result{}, fmt.Errorf("connect over cdp: %w", err)
	}
	defer func() {
		_ = browser.Close()
	}()

	page, err := ensureReplayPage(browser)
	if err != nil {
		return Result{}, err
	}

	cdpSession, err := page.Context().NewCDPSession(page)
	if err != nil {
		return Result{}, fmt.Errorf("create page cdp session: %w", err)
	}
	defer func() {
		_ = cdpSession.Detach()
	}()

	_, _ = cdpSession.Send("DOM.enable", nil)
	_, _ = cdpSession.Send("Page.enable", nil)
	_, _ = cdpSession.Send("Runtime.enable", nil)

	result := Result{Errors: make([]string, 0)}

	for _, action := range export.Actions {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		err := replayAction(page, cdpSession, action)
		if err != nil {
			result.FailedActions++
			result.Errors = append(result.Errors, fmt.Sprintf("%s[%d]: %v", action.Type, action.OrderIndex, err))
		} else {
			result.ExecutedActions++
		}

		if options.ActionDelay > 0 {
			time.Sleep(options.ActionDelay)
		}
	}

	return result, nil
}

func ensureReplayPage(browser playwright.Browser) (playwright.Page, error) {
	contexts := browser.Contexts()
	for _, browserContext := range contexts {
		pages := browserContext.Pages()
		if len(pages) > 0 {
			return pages[0], nil
		}
	}

	context, err := browser.NewContext()
	if err != nil {
		return nil, fmt.Errorf("create replay context: %w", err)
	}

	page, err := context.NewPage()
	if err != nil {
		return nil, fmt.Errorf("create replay page: %w", err)
	}

	return page, nil
}

func replayAction(page playwright.Page, cdpSession playwright.CDPSession, action Action) error {
	switch action.Type {
	case "navigate":
		url := toString(action.Payload["url"])
		if strings.TrimSpace(url) == "" {
			url = action.FrameURL
		}
		if strings.TrimSpace(url) == "" {
			return fmt.Errorf("navigate action missing url")
		}
		_, err := page.Goto(url)
		return err
	case "click", "dblclick":
		return replayClick(page, cdpSession, action)
	case "type", "change":
		return replayType(page, action)
	case "keydown":
		key := toString(action.Payload["key"])
		if key == "" {
			return nil
		}
		return page.Keyboard().Down(key)
	case "keyup":
		key := toString(action.Payload["key"])
		if key == "" {
			return nil
		}
		return page.Keyboard().Up(key)
	case "scroll":
		deltaY := toFloat64(action.Payload["scrollY"])
		return page.Mouse().Wheel(0, deltaY)
	case "focus":
		locator, _, err := resolveBestLocator(page, action.Payload, action.Type)
		if err != nil {
			return err
		}
		return locator.Focus()
	default:
		return nil
	}
}

func replayClick(page playwright.Page, cdpSession playwright.CDPSession, action Action) error {
	resolvedLocator, chosen, err := resolveBestLocator(page, action.Payload, action.Type)
	if err != nil {
		return err
	}

	clickCount := 1
	if action.Type == "dblclick" {
		clickCount = 2
	}

	if pointerPayload := mapFromAny(action.Payload["pointer"]); pointerPayload != nil {
		if rawClickCount := toInt(pointerPayload["clickCount"]); rawClickCount > 0 {
			clickCount = rawClickCount
		}
	}

	if chosen.Kind == "css" || chosen.Kind == "dompath" {
		if err := dispatchClickViaCDP(cdpSession, chosen.Value, clickCount, pointerButton(action.Payload)); err == nil {
			return nil
		}
	}

	return resolvedLocator.Click(playwright.LocatorClickOptions{ClickCount: playwright.Int(clickCount)})
}

func replayType(page playwright.Page, action Action) error {
	resolvedLocator, _, err := resolveBestLocator(page, action.Payload, action.Type)
	if err != nil {
		return err
	}

	value := toString(action.Payload["value"])
	if strings.HasPrefix(value, "[REDACTED") || value == "" {
		return nil
	}

	return resolvedLocator.Fill(value)
}

func resolveBestLocator(page playwright.Page, payload map[string]interface{}, actionType string) (playwright.Locator, locator, error) {
	locators := extractLocators(payload, actionType)
	if len(locators) == 0 {
		return nil, locator{}, fmt.Errorf("no locators found in action payload")
	}

	for _, candidateLocator := range locators {
		candidate := buildPlaywrightLocator(page, candidateLocator)
		if candidate == nil {
			continue
		}

		count, err := candidate.Count()
		if err != nil || count == 0 {
			continue
		}

		return candidate.First(), candidateLocator, nil
	}

	return nil, locator{}, fmt.Errorf("unable to resolve locator")
}

func buildPlaywrightLocator(page playwright.Page, resolved locator) playwright.Locator {
	switch resolved.Kind {
	case "role":
		options := playwright.PageGetByRoleOptions{}
		if strings.TrimSpace(resolved.Name) != "" {
			options.Name = resolved.Name
		}
		return page.GetByRole(playwright.AriaRole(resolved.Role), options)
	case "text":
		exact := resolved.Match == "exact"
		return page.GetByText(resolved.Value, playwright.PageGetByTextOptions{Exact: playwright.Bool(exact)})
	case "xpath":
		return page.Locator("xpath=" + resolved.Value)
	case "css", "dompath":
		return page.Locator(resolved.Value)
	default:
		return nil
	}
}

func extractLocators(payload map[string]interface{}, actionType string) []locator {
	if payload == nil {
		return nil
	}

	raw, ok := payload["locators"].([]interface{})
	if !ok {
		return nil
	}

	locators := make([]locator, 0, len(raw))
	for _, item := range raw {
		asMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		normalized := normalizeLocator(locator{
			Kind:  strings.TrimSpace(strings.ToLower(toString(asMap["kind"]))),
			Value: strings.TrimSpace(toString(asMap["value"])),
			Role:  strings.TrimSpace(toString(asMap["role"])),
			Name:  strings.TrimSpace(toString(asMap["name"])),
			Match: strings.TrimSpace(strings.ToLower(toString(asMap["match"]))),
		})
		if normalized.Kind == "" {
			continue
		}
		if shouldSkipLocatorForAction(normalized, actionType) {
			continue
		}

		locators = append(locators, normalized)
	}

	return rankAndDedupeLocators(locators, actionType)
}

func normalizeLocator(input locator) locator {
	switch input.Kind {
	case "role":
		if input.Role == "" {
			return locator{}
		}
		return input
	case "css", "xpath", "dompath", "text":
		if input.Value == "" {
			return locator{}
		}
		if input.Kind == "text" && input.Match == "" {
			input.Match = "contains"
		}
		return input
	default:
		return locator{}
	}
}

func rankAndDedupeLocators(locators []locator, actionType string) []locator {
	sort.SliceStable(locators, func(i int, j int) bool {
		left := locatorPriority(locators[i], actionType)
		right := locatorPriority(locators[j], actionType)
		if left == right {
			return locatorKey(locators[i]) < locatorKey(locators[j])
		}
		return left < right
	})

	seen := map[string]struct{}{}
	unique := make([]locator, 0, len(locators))
	for _, item := range locators {
		key := locatorKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, item)
	}

	return unique
}

func locatorPriority(input locator, actionType string) int {
	normalizedAction := strings.TrimSpace(strings.ToLower(actionType))
	if normalizedAction == "type" || normalizedAction == "change" || normalizedAction == "focus" {
		switch input.Kind {
		case "css":
			return 1
		case "xpath":
			return 2
		case "dompath":
			return 3
		case "role":
			return 4
		case "text":
			return 5
		default:
			return 99
		}
	}

	switch input.Kind {
	case "role":
		return 1
	case "text":
		return 2
	case "css":
		return 3
	case "xpath":
		return 4
	case "dompath":
		return 5
	default:
		return 99
	}
}

func shouldSkipLocatorForAction(input locator, actionType string) bool {
	normalizedAction := strings.TrimSpace(strings.ToLower(actionType))
	if normalizedAction != "type" && normalizedAction != "change" && normalizedAction != "focus" {
		return false
	}

	// A role locator without accessible name is too broad for input-like actions
	// and commonly resolves to the first textbox on the page.
	if input.Kind == "role" && strings.TrimSpace(input.Name) == "" {
		return true
	}

	// Text locators are usually not stable for typing targets.
	if input.Kind == "text" {
		return true
	}

	return false
}

func locatorKey(input locator) string {
	return strings.Join([]string{input.Kind, input.Value, input.Role, input.Name, input.Match}, "|")
}

func dispatchClickViaCDP(session playwright.CDPSession, selector string, clickCount int, button string) error {
	x, y, err := queryElementCenter(session, selector)
	if err != nil {
		return err
	}

	if button == "" {
		button = "left"
	}
	if clickCount <= 0 {
		clickCount = 1
	}

	if _, err := session.Send("Input.dispatchMouseEvent", map[string]interface{}{
		"type":   "mouseMoved",
		"x":      x,
		"y":      y,
		"button": "none",
	}); err != nil {
		return err
	}

	if _, err := session.Send("Input.dispatchMouseEvent", map[string]interface{}{
		"type":       "mousePressed",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
	}); err != nil {
		return err
	}

	if _, err := session.Send("Input.dispatchMouseEvent", map[string]interface{}{
		"type":       "mouseReleased",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
	}); err != nil {
		return err
	}

	return nil
}

func queryElementCenter(session playwright.CDPSession, selector string) (float64, float64, error) {
	docResp, err := session.Send("DOM.getDocument", map[string]interface{}{
		"depth":  1,
		"pierce": true,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("get document root: %w", err)
	}

	rootNodeID := toInt(mapFromAny(mapFromAny(docResp)["root"])["nodeId"])
	if rootNodeID <= 0 {
		return 0, 0, fmt.Errorf("invalid root node id")
	}

	queryResp, err := session.Send("DOM.querySelector", map[string]interface{}{
		"nodeId":   rootNodeID,
		"selector": selector,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("query selector: %w", err)
	}

	nodeID := toInt(mapFromAny(queryResp)["nodeId"])
	if nodeID <= 0 {
		return 0, 0, fmt.Errorf("selector did not resolve to a node")
	}

	boxResp, err := session.Send("DOM.getBoxModel", map[string]interface{}{"nodeId": nodeID})
	if err != nil {
		return 0, 0, fmt.Errorf("get box model: %w", err)
	}

	model := mapFromAny(mapFromAny(boxResp)["model"])
	contentAny, ok := model["content"].([]interface{})
	if !ok || len(contentAny) < 8 {
		return 0, 0, fmt.Errorf("invalid box model content points")
	}

	x := (toFloat64(contentAny[0]) + toFloat64(contentAny[2]) + toFloat64(contentAny[4]) + toFloat64(contentAny[6])) / 4
	y := (toFloat64(contentAny[1]) + toFloat64(contentAny[3]) + toFloat64(contentAny[5]) + toFloat64(contentAny[7])) / 4

	return x, y, nil
}

func pointerButton(payload map[string]interface{}) string {
	pointer := mapFromAny(payload["pointer"])
	button := toInt(pointer["button"])
	switch button {
	case 1:
		return "middle"
	case 2:
		return "right"
	default:
		return "left"
	}
}

func toString(input interface{}) string {
	if input == nil {
		return ""
	}
	if value, ok := input.(string); ok {
		return value
	}
	return fmt.Sprintf("%v", input)
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
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func toInt(input interface{}) int {
	switch value := input.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
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
