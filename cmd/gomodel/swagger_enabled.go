//go:build swagger

package main

import swaggerdocs "github.com/enterpilot/gomodel/cmd/gomodel/docs"

func configureSwaggerDocs(basePath string) {
	swaggerdocs.SwaggerInfo.BasePath = basePath
}
