package server

import (
	"net/http"
	"net/url"
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
			strippedRaw := strippedRawPath(req.URL.RawPath, basePath)
			if req.URL.RawPath != "" && strippedRaw == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid encoded request path")
			}
			urlCopy.Path = strippedPath
			urlCopy.RawPath = strippedRaw
			cloned.URL = &urlCopy
			cloned.RequestURI = strippedRequestURI(strippedPath, urlCopy.RawPath)
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

func strippedRawPath(rawPath, basePath string) string {
	if rawPath == "" {
		return ""
	}
	stripped, ok := stripBasePath(rawPath, basePath)
	if !ok {
		return stripRawPathByDecodedBase(rawPath, basePath)
	}
	return stripped
}

func stripRawPathByDecodedBase(rawPath, basePath string) string {
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		return ""
	}
	if _, ok := stripBasePath(decoded, basePath); !ok {
		return ""
	}
	basePath = config.NormalizeBasePath(basePath)
	if basePath == "/" {
		return rawPath
	}
	rawParts := strings.Split(rawPath, "/")
	baseParts := strings.Split(strings.Trim(basePath, "/"), "/")
	if len(rawParts) == 0 || rawParts[0] != "" || len(rawParts) < len(baseParts)+1 {
		return ""
	}
	for _, rawPart := range rawParts[1 : len(baseParts)+1] {
		if strings.Contains(strings.ToLower(rawPart), "%2f") {
			return ""
		}
	}
	suffixParts := rawParts[len(baseParts)+1:]
	if len(suffixParts) == 0 {
		return "/"
	}
	return "/" + strings.Join(suffixParts, "/")
}

func strippedRequestURI(pathValue, rawPath string) string {
	if rawPath != "" {
		return rawPath
	}
	return pathValue
}
