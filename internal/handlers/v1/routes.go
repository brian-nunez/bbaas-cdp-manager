package v1

import (
	"github.com/brian-nunez/go-echo-starter-template/internal/browser"
	"github.com/labstack/echo/v4"
)

type Dependencies struct {
	BrowserManager *browser.Manager
}

func RegisterRoutes(e *echo.Echo, deps Dependencies) {
	browserHandlers := NewBrowserHandler(deps.BrowserManager)

	v1Group := e.Group("/api/v1")
	v1Group.GET("/health", HealthHandler)

	browsersGroup := v1Group.Group("/browsers")
	browsersGroup.POST("", browserHandlers.Create)
	browsersGroup.GET("", browserHandlers.List)
	browsersGroup.GET("/:id", browserHandlers.Get)
	browsersGroup.POST("/:id/keepalive", browserHandlers.KeepAlive)
	browsersGroup.DELETE("/:id", browserHandlers.Delete)
}
