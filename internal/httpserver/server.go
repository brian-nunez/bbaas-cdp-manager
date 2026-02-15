package httpserver

import (
	"context"

	v1 "github.com/brian-nunez/bbaas-cdp-manager/internal/handlers/v1"
	"github.com/labstack/echo/v4"
)

type Server interface {
	Start(addr string) error
	Shutdown(ctx context.Context) error
}

type BootstrapConfig struct {
	StaticDirectories map[string]string
	V1Dependencies    v1.Dependencies
}

func Bootstrap(config BootstrapConfig) Server {
	server := New().
		WithStaticAssets(config.StaticDirectories).
		WithDefaultMiddleware().
		WithErrorHandler().
		WithRoutes(func(e *echo.Echo) {
			v1.RegisterRoutes(e, config.V1Dependencies)
		}).
		WithNotFound().
		Build()

	return server
}
