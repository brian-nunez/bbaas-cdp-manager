package recorder

import "time"

type RedactionMode string

const (
	RedactionModeOff        RedactionMode = "off"
	RedactionModeMaskInputs RedactionMode = "maskInputs"
	RedactionModeMaskAll    RedactionMode = "maskAllText"
)

type StartRecordingOptions struct {
	RedactionMode RedactionMode `json:"redactionMode"`
	IncludeScroll *bool         `json:"includeScroll,omitempty"`
	IncludeKeys   *bool         `json:"includeKeys,omitempty"`
}

func (o StartRecordingOptions) WithDefaults() StartRecordingOptions {
	out := o
	if out.RedactionMode == "" {
		out.RedactionMode = RedactionModeMaskInputs
	}

	if out.IncludeScroll == nil {
		defaultIncludeScroll := true
		out.IncludeScroll = &defaultIncludeScroll
	}

	if out.IncludeKeys == nil {
		defaultIncludeKeys := true
		out.IncludeKeys = &defaultIncludeKeys
	}

	return out
}

func (o StartRecordingOptions) IsValid() bool {
	switch o.RedactionMode {
	case RedactionModeOff, RedactionModeMaskInputs, RedactionModeMaskAll:
		return true
	default:
		return false
	}
}

type Locator struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Role  string `json:"role,omitempty"`
	Name  string `json:"name,omitempty"`
	Match string `json:"match,omitempty"`
}

type Recording struct {
	ID          string                 `json:"id"`
	BrowserID   string                 `json:"browserId"`
	Status      string                 `json:"status"`
	CreatedAt   time.Time              `json:"createdAt"`
	StartedAt   time.Time              `json:"startedAt"`
	StoppedAt   *time.Time             `json:"stoppedAt,omitempty"`
	StopReason  string                 `json:"stopReason,omitempty"`
	Meta        map[string]interface{} `json:"meta,omitempty"`
	ActionCount int                    `json:"actionCount,omitempty"`
}

type Action struct {
	ID            string                 `json:"id"`
	RecordingID   string                 `json:"recordingId"`
	TSMonotonicMS float64                `json:"tsMonotonicMs"`
	TSWallISO     string                 `json:"tsWallIso"`
	Type          string                 `json:"type"`
	FrameURL      string                 `json:"frameUrl"`
	Payload       map[string]interface{} `json:"payload"`
	OrderIndex    int                    `json:"orderIndex"`
}

type RecordingExport struct {
	Recording Recording `json:"recording"`
	Actions   []Action  `json:"actions"`
}

type StopRecordingResult struct {
	Recording   Recording `json:"recording"`
	ActionCount int       `json:"actionCount"`
}

type ReplayRecordingRequest struct {
	BrowserID      string `json:"browserId"`
	ActionDelayMS  int    `json:"actionDelayMs,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type ReplayRecordingResult struct {
	RecordingID     string   `json:"recordingId"`
	BrowserID       string   `json:"browserId"`
	ExecutedActions int      `json:"executedActions"`
	FailedActions   int      `json:"failedActions"`
	Errors          []string `json:"errors,omitempty"`
}
