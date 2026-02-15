package uihandlers

import (
	"context"

	"github.com/brian-nunez/go-echo-starter-template/views/pages"
	"github.com/labstack/echo/v4"
)

func HomeHandler(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return pages.Home().Render(context.Background(), c.Response().Writer)
}
