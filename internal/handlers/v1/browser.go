package v1

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/brian-nunez/go-echo-starter-template/internal/browser"
	"github.com/labstack/echo/v4"
)

type BrowserHandler struct {
	manager *browser.Manager
}

type createBrowserRequest struct {
	Headless           *bool  `json:"headless"`
	IdleTimeoutSeconds *int64 `json:"idleTimeoutSeconds"`
}

type listBrowsersResponse struct {
	Browsers []browser.BrowserInfo `json:"browsers"`
}

func NewBrowserHandler(manager *browser.Manager) *BrowserHandler {
	return &BrowserHandler{
		manager: manager,
	}
}

func (h *BrowserHandler) Create(c echo.Context) error {
	if h.manager == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("browser manager is not available"))
	}

	req := createBrowserRequest{}
	if err := c.Bind(&req); err != nil && !errors.Is(err, io.EOF) {
		return c.JSON(http.StatusBadRequest, errorResponse("invalid request payload"))
	}

	if req.IdleTimeoutSeconds != nil && *req.IdleTimeoutSeconds <= 0 {
		return c.JSON(http.StatusBadRequest, errorResponse("idleTimeoutSeconds must be greater than 0"))
	}

	params := browser.CreateBrowserParams{
		Headless: req.Headless,
	}

	if req.IdleTimeoutSeconds != nil {
		params.IdleTimeout = time.Duration(*req.IdleTimeoutSeconds) * time.Second
	}

	result, err := h.manager.CreateBrowser(c.Request().Context(), params)
	if err != nil {
		return mapManagerError(c, err)
	}

	return c.JSON(http.StatusCreated, result)
}

func (h *BrowserHandler) List(c echo.Context) error {
	if h.manager == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("browser manager is not available"))
	}

	browsers, err := h.manager.ListBrowsers()
	if err != nil {
		return mapManagerError(c, err)
	}

	return c.JSON(http.StatusOK, listBrowsersResponse{Browsers: browsers})
}

func (h *BrowserHandler) Get(c echo.Context) error {
	if h.manager == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("browser manager is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	if browserID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id is required"))
	}

	info, err := h.manager.GetBrowser(browserID)
	if err != nil {
		return mapManagerError(c, err)
	}

	return c.JSON(http.StatusOK, info)
}

func (h *BrowserHandler) KeepAlive(c echo.Context) error {
	if h.manager == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("browser manager is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	if browserID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id is required"))
	}

	info, err := h.manager.KeepAlive(browserID)
	if err != nil {
		return mapManagerError(c, err)
	}

	return c.JSON(http.StatusOK, info)
}

func (h *BrowserHandler) Delete(c echo.Context) error {
	if h.manager == nil {
		return c.JSON(http.StatusServiceUnavailable, errorResponse("browser manager is not available"))
	}

	browserID := strings.TrimSpace(c.Param("id"))
	if browserID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse("browser id is required"))
	}

	result, err := h.manager.CloseBrowser(c.Request().Context(), browserID, false)
	if err != nil {
		return mapManagerError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

func mapManagerError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, browser.ErrBrowserNotFound):
		return c.JSON(http.StatusNotFound, errorResponse("browser not found"))
	case errors.Is(err, browser.ErrManagerNotStarted):
		return c.JSON(http.StatusServiceUnavailable, errorResponse("browser manager is not started"))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return c.JSON(http.StatusRequestTimeout, errorResponse("request timed out"))
	default:
		return c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
	}
}

func errorResponse(message string) map[string]string {
	return map[string]string{
		"error": message,
	}
}
