//go:build swagger

package main

import swaggerdocs "github.com/ENTERPILOT/GoModel/cmd/gomodel/docs"

func configureSwaggerDocs(basePath string) {
	swaggerdocs.SwaggerInfo.BasePath = basePath
}
