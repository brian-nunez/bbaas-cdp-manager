package v1

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/brian-nunez/bbaas-cdp-manager/internal/browser"
	"github.com/brian-nunez/bbaas-cdp-manager/internal/recorder"
	"github.com/labstack/echo/v4"
)

type RecordingsHandler struct {
	recorderService *recorder.Service
}

type startRecordingRequest struct {
	RedactionMode recorder.RedactionMode `json:"redactionMode"`
	IncludeScroll *bool                  `json:"includeScroll"`
	IncludeKeys   *bool                  `json:"includeKeys"`
}

type replayRecordingRequest struct {
	BrowserID      string `json:"browserId"`
	ActionDelayMS  int    `json:"actionDelayMs"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

func NewRecordingsHandler(recorderService *recorder.Service) *RecordingsHandler {
	return &RecordingsHandler{recorderService: recorderService}
}

func (h *RecordingsHandler) Start(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	if browserID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id is required"))
	}

	request := startRecordingRequest{}
	if err := c.Bind(&request); err != nil && !errors.Is(err, io.EOF) {
		return c.JSON(http.StatusBadRequest, errorResponse("invalid request payload"))
	}

	recording, err := h.recorderService.StartRecording(c.Request().Context(), browserID, recorder.StartRecordingOptions{
		RedactionMode: request.RedactionMode,
		IncludeScroll: request.IncludeScroll,
		IncludeKeys:   request.IncludeKeys,
	})
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{"recording": recording})
}

func (h *RecordingsHandler) Stop(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if browserID == "" || recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id and recording id are required"))
	}

	result, err := h.recorderService.StopRecording(c.Request().Context(), browserID, recordingID, "stopped_by_user")
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"recording":   result.Recording,
		"actionCount": result.ActionCount,
		"stoppedAt":   result.Recording.StoppedAt,
	})
}

func (h *RecordingsHandler) List(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	if browserID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id is required"))
	}

	recordings, err := h.recorderService.ListRecordings(c.Request().Context(), browserID)
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"recordings": recordings,
	})
}

func (h *RecordingsHandler) Get(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if browserID == "" || recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id and recording id are required"))
	}

	recording, err := h.recorderService.GetRecording(c.Request().Context(), browserID, recordingID)
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"recording": recording,
	})
}

func (h *RecordingsHandler) ListActions(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if browserID == "" || recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id and recording id are required"))
	}

	limit := parseIntOrDefault(c.QueryParam("limit"), 200)
	offset := parseIntOrDefault(c.QueryParam("offset"), 0)
	if limit <= 0 || offset < 0 {
		return c.JSON(http.StatusBadRequest, errorResponse("limit must be > 0 and offset must be >= 0"))
	}

	actions, err := h.recorderService.ListActions(c.Request().Context(), browserID, recordingID, limit, offset)
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"actions": actions,
		"limit":   limit,
		"offset":  offset,
	})
}

func (h *RecordingsHandler) Export(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if browserID == "" || recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id and recording id are required"))
	}

	export, err := h.recorderService.ExportRecording(c.Request().Context(), browserID, recordingID)
	if err != nil {
		return mapRecorderError(c, err)
	}

	filename := "recording_" + recordingID + ".json"
	c.Response().Header().Set("Content-Disposition", "attachment; filename="+filename)

	return c.JSON(http.StatusOK, export)
}

func (h *RecordingsHandler) GetByID(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("recording id is required"))
	}

	recording, err := h.recorderService.GetRecordingByID(c.Request().Context(), recordingID)
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"recording": recording})
}

func (h *RecordingsHandler) StopByID(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("recording id is required"))
	}

	result, err := h.recorderService.StopRecordingByID(c.Request().Context(), recordingID, "stopped_by_user")
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"recording":   result.Recording,
		"actionCount": result.ActionCount,
		"stoppedAt":   result.Recording.StoppedAt,
	})
}

func (h *RecordingsHandler) ListActionsByID(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("recording id is required"))
	}

	limit := parseIntOrDefault(c.QueryParam("limit"), 200)
	offset := parseIntOrDefault(c.QueryParam("offset"), 0)
	if limit <= 0 || offset < 0 {
		return c.JSON(http.StatusBadRequest, errorResponse("limit must be > 0 and offset must be >= 0"))
	}

	actions, err := h.recorderService.ListActionsByID(c.Request().Context(), recordingID, limit, offset)
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"actions": actions,
		"limit":   limit,
		"offset":  offset,
	})
}

func (h *RecordingsHandler) ExportByID(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("recording id is required"))
	}

	exported, err := h.recorderService.ExportRecordingByID(c.Request().Context(), recordingID)
	if err != nil {
		return mapRecorderError(c, err)
	}

	filename := "recording_" + recordingID + ".json"
	c.Response().Header().Set("Content-Disposition", "attachment; filename="+filename)

	return c.JSON(http.StatusOK, exported)
}

func (h *RecordingsHandler) ReplayByID(c echo.Context) error {
	if h.recorderService == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("recorder service is not available"))
	}

	recordingID := strings.TrimSpace(c.Param("recordingId"))
	if recordingID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("recording id is required"))
	}

	request := replayRecordingRequest{}
	if err := c.Bind(&request); err != nil && !errors.Is(err, io.EOF) {
		return c.JSON(http.StatusBadRequest, errorResponse("invalid request payload"))
	}

	result, err := h.recorderService.ReplayRecording(c.Request().Context(), recordingID, recorder.ReplayRecordingRequest{
		BrowserID:      strings.TrimSpace(request.BrowserID),
		ActionDelayMS:  request.ActionDelayMS,
		TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		return mapRecorderError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"replay": result})
}

func mapRecorderError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, recorder.ErrRecordingNotFound), errors.Is(err, browser.ErrBrowserNotFound):
		return c.JSON(http.StatusNotFound, errorResponse("recording or browser not found"))
	case errors.Is(err, recorder.ErrRecordingAlreadyOpen):
		return c.JSON(http.StatusConflict, errorResponse("recording already active for browser"))
	case errors.Is(err, recorder.ErrInvalidOptions):
		return c.JSON(http.StatusBadRequest, errorResponse("invalid recording options"))
	default:
		return mapManagerError(c, err)
	}
}

func parseIntOrDefault(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}

	return parsed
}
