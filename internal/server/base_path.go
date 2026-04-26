package server

import (
	"net/http"
	"strings"

	"gomodel/config"

	"github.com/labstack/echo/v5"
)

func configuredBasePath(cfg *Config) string {
	if cfg == nil {
		return "/"
	}
	return config.NormalizeBasePath(cfg.BasePath)
}

func stripBasePathMiddleware(basePath string) echo.MiddlewareFunc {
	basePath = config.NormalizeBasePath(basePath)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if basePath == "/" {
				return next(c)
			}

			req := c.Request()
			strippedPath, ok := stripBasePath(req.URL.Path, basePath)
			if !ok {
				return echo.NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
			}

			cloned := req.Clone(req.Context())
			urlCopy := *req.URL
			urlCopy.Path = strippedPath
			urlCopy.RawPath = ""
			cloned.URL = &urlCopy
			cloned.RequestURI = strippedPath
			if urlCopy.RawQuery != "" {
				cloned.RequestURI += "?" + urlCopy.RawQuery
			}
			c.SetRequest(cloned)
			return next(c)
		}
	}
}

func stripBasePath(requestPath, basePath string) (string, bool) {
	basePath = config.NormalizeBasePath(basePath)
	if basePath == "/" {
		if requestPath == "" {
			return "/", true
		}
		return requestPath, true
	}
	if requestPath == basePath {
		return "/", true
	}
	prefix := basePath + "/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", false
	}
	stripped := strings.TrimPrefix(requestPath, basePath)
	if stripped == "" {
		return "/", true
	}
	return stripped, true
}
