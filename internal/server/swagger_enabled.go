//go:build swagger

package server

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v5"
	swaggerFiles "github.com/swaggo/files/v2"
	"github.com/swaggo/swag/v2"
	"sigs.k8s.io/yaml"
)

// SwaggerAvailable reports whether this binary was built with Swagger UI support.
func SwaggerAvailable() bool {
	return true
}

func registerSwagger(e *echo.Echo, cfg *Config) {
	if cfg != nil && cfg.SwaggerEnabled {
		e.GET("/swagger/*", serveSwagger)
	}
}

func serveSwagger(c *echo.Context) error {
	if c.Request().Method != http.MethodGet {
		return echo.NewHTTPError(http.StatusMethodNotAllowed, http.StatusText(http.StatusMethodNotAllowed))
	}

	resourcePath, ok := swaggerResourcePath(c.Request().URL.Path)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
	}
	if resourcePath == "" {
		return c.Redirect(http.StatusMovedPermanently, "index.html")
	}

	switch resourcePath {
	case "index.html":
		return c.HTMLBlob(http.StatusOK, []byte(swaggerIndexHTML))
	case "doc.json":
		doc, err := swag.ReadDoc(swag.Name)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSONBlob(http.StatusOK, []byte(doc))
	case "doc.yaml":
		doc, err := swag.ReadDoc(swag.Name)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		yamlDoc, err := yaml.JSONToYAML([]byte(doc))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.Blob(http.StatusOK, "text/plain; charset=utf-8", yamlDoc)
	default:
		return serveSwaggerAsset(c, resourcePath)
	}
}

func swaggerResourcePath(requestPath string) (string, bool) {
	resourcePath, ok := strings.CutPrefix(requestPath, "/swagger/")
	if !ok {
		return "", false
	}
	for _, part := range strings.Split(resourcePath, "/") {
		if part == ".." {
			return "", false
		}
	}
	if resourcePath == "" {
		return "", true
	}
	return path.Clean(resourcePath), true
}

func serveSwaggerAsset(c *echo.Context, resourcePath string) error {
	file, err := swaggerFiles.FS.Open(resourcePath)
	if errors.Is(err, fs.ErrNotExist) {
		return echo.NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	if stat.IsDir() {
		return echo.NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
	}

	return c.Stream(http.StatusOK, swaggerContentType(resourcePath), file)
}

func swaggerContentType(resourcePath string) string {
	switch path.Ext(resourcePath) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	default:
		if contentType := mime.TypeByExtension(path.Ext(resourcePath)); contentType != "" {
			return contentType
		}
		return "application/octet-stream"
	}
}

const swaggerIndexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Swagger UI</title>
  <link rel="stylesheet" href="./swagger-ui.css">
  <link rel="icon" type="image/png" href="./favicon-32x32.png" sizes="32x32">
  <link rel="icon" type="image/png" href="./favicon-16x16.png" sizes="16x16">
  <style>
    html {
      box-sizing: border-box;
      overflow-y: scroll;
    }
    *, *:before, *:after {
      box-sizing: inherit;
    }
    body {
      margin: 0;
      background: #fafafa;
    }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="./swagger-ui-bundle.js"></script>
  <script src="./swagger-ui-standalone-preset.js"></script>
  <script>
    window.onload = function() {
      window.ui = SwaggerUIBundle({
        urls: [
          { name: "doc.json", url: "doc.json" },
          { name: "doc.yaml", url: "doc.yaml" }
        ],
        dom_id: "#swagger-ui",
        deepLinking: true,
        docExpansion: "list",
        persistAuthorization: false,
        syntaxHighlight: true,
        validatorUrl: null,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset
        ],
        plugins: [
          SwaggerUIBundle.plugins.DownloadUrl
        ],
        layout: "StandaloneLayout"
      })
    }
  </script>
</body>
</html>
`
