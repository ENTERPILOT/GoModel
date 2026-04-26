//go:build swagger

package server

import (
	"github.com/labstack/echo/v5"
	echoswagger "github.com/swaggo/echo-swagger"
)

// SwaggerAvailable reports whether this binary was built with Swagger UI support.
func SwaggerAvailable() bool {
	return true
}

func registerSwagger(e *echo.Echo, cfg *Config) {
	if cfg != nil && cfg.SwaggerEnabled {
		e.GET("/swagger/*", echoswagger.WrapHandler)
	}
}
