package echotest

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequest_EncodesBodyAndBindsOptions(t *testing.T) {
	c, rec := Request(t, http.MethodPut, "/items/:id?verbose=1", map[string]string{"name": "a"},
		WithPathValue("id", "42"),
		WithHeader("X-Test", "yes"),
		WithValue("user", "u1"),
		WithPath("/items/:id"),
	)

	assert.Equal(t, http.MethodPut, c.Request().Method)
	assert.Equal(t, "42", c.Param("id"))
	assert.Equal(t, "1", c.QueryParam("verbose"))
	assert.Equal(t, "yes", c.Request().Header.Get("X-Test"))
	assert.Equal(t, echo.MIMEApplicationJSON, c.Request().Header.Get(echo.HeaderContentType))
	assert.Equal(t, "u1", c.Get("user"))
	assert.Equal(t, "/items/:id", c.Path())

	var body map[string]string
	require.NoError(t, c.Bind(&body))
	assert.Equal(t, "a", body["name"])

	require.NoError(t, c.JSON(http.StatusOK, map[string]int{"n": 1}))
	assert.Equal(t, 1, Decode[map[string]int](t, rec)["n"])
}

func TestRequest_RawBodiesAndContentType(t *testing.T) {
	c, _ := Post(t, "/x", `{"raw":true}`, WithContentType("text/plain"))
	assert.Equal(t, "text/plain", c.Request().Header.Get(echo.HeaderContentType))

	c, _ = Get(t, "/x")
	assert.Empty(t, c.Request().Header.Get(echo.HeaderContentType))
	assert.Equal(t, http.NoBody, c.Request().Body)
}
