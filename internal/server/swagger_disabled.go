//go:build !swagger

package server

import "github.com/labstack/echo/v5"

// SwaggerAvailable reports whether this binary was built with Swagger UI support.
func SwaggerAvailable() bool {
	return false
}

func registerSwagger(_ *echo.Echo, _ *Config) {}
