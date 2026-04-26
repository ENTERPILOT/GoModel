//go:build swagger

package main

import swaggerdocs "gomodel/cmd/gomodel/docs"

func configureSwaggerDocs(basePath string) {
	swaggerdocs.SwaggerInfo.BasePath = basePath
}
