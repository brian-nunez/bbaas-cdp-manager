package v1

import (
	"github.com/brian-nunez/bbaas-cdp-manager/internal/browser"
	"github.com/brian-nunez/bbaas-cdp-manager/internal/recorder"
	"github.com/labstack/echo/v4"
)

type Dependencies struct {
	BrowserManager *browser.Manager
	Recorder       *recorder.Service
}

func RegisterRoutes(e *echo.Echo, deps Dependencies) {
	browserHandlers := NewBrowserHandler(deps.BrowserManager)
	recordingHandlers := NewRecordingsHandler(deps.Recorder)

	v1Group := e.Group("/api/v1")
	v1Group.GET("/health", HealthHandler)

	browsersGroup := v1Group.Group("/browsers")
	browsersGroup.POST("", browserHandlers.Create)
	browsersGroup.GET("", browserHandlers.List)
	browsersGroup.GET("/:id", browserHandlers.Get)
	browsersGroup.POST("/:id/keepalive", browserHandlers.KeepAlive)
	browsersGroup.DELETE("/:id", browserHandlers.Delete)
	browsersGroup.POST("/:id/recordings", recordingHandlers.Start)
	browsersGroup.POST("/:id/recordings/:recordingId/stop", recordingHandlers.Stop)
	browsersGroup.GET("/:id/recordings", recordingHandlers.List)
	browsersGroup.GET("/:id/recordings/:recordingId", recordingHandlers.Get)
	browsersGroup.GET("/:id/recordings/:recordingId/actions", recordingHandlers.ListActions)
	browsersGroup.GET("/:id/recordings/:recordingId/export", recordingHandlers.Export)

	recordingsGroup := v1Group.Group("/recordings")
	recordingsGroup.GET("/:recordingId", recordingHandlers.GetByID)
	recordingsGroup.POST("/:recordingId/stop", recordingHandlers.StopByID)
	recordingsGroup.GET("/:recordingId/actions", recordingHandlers.ListActionsByID)
	recordingsGroup.GET("/:recordingId/export", recordingHandlers.ExportByID)
	recordingsGroup.POST("/:recordingId/replay", recordingHandlers.ReplayByID)
}
