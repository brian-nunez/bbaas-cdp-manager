package recorder

import "errors"

var (
	ErrRecordingNotFound    = errors.New("recording not found")
	ErrRecordingAlreadyOpen = errors.New("recording already active for browser")
	ErrInvalidOptions       = errors.New("invalid recording options")
)
