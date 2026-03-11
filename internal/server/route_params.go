package server

import "github.com/labstack/echo/v5"

func routeParamsMap(values echo.PathValues) map[string]string {
	if len(values) == 0 {
		return nil
	}
	params := make(map[string]string, len(values))
	for _, item := range values {
		params[item.Name] = item.Value
	}
	return params
}
