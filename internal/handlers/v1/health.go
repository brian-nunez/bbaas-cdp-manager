package v1

import (
	"github.com/labstack/echo/v4"
)

func HealthHandler(c echo.Context) error {
	return c.JSON(200, map[string]string{
		"status":  "ok",
		"service": "browser-cdp-manager",
	})
}
