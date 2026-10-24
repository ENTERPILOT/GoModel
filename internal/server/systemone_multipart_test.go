package server

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes and jpegBytes start with their format's signature, which is what
// content sniffing reads.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
)

type formPart struct {
	name, contentType string
	data              []byte
}

// systemOneForm builds a multipart body: the JSON request part first, then
// the given parts.
func systemOneForm(t *testing.T, request string, parts ...formPart) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="request"`)
	header.Set("Content-Type", "application/json")
	pw, err := w.CreatePart(header)
	require.NoError(t, err)
	_, err = pw.Write([]byte(request))
	require.NoError(t, err)
	for _, part := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+part.name+`"; filename="image"`)
		if part.contentType != "" {
			header.Set("Content-Type", part.contentType)
		}
		pw, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = pw.Write(part.data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

const multipartRequest = `{"model":"kev/kev-latest","state":{"door":"ref-door"},` +
	`"assets":{"ref-door":{"type":"image","part":"door-photo"}},` +
	`"questions":{"zz":{"type":"noul","instructions":"Is there a person in ` + "`door`" + `?"},"aa":{"type":"noul","instructions":"?"}}}`

// TypeSafe's multipart form becomes the equivalent JSON request: each image
// part is inlined into the asset that names it, and the rest of the request,
// the questions' order included, is kept as sent.
func TestSystemOneMultipartToJSON_InlinesImageParts(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		data        []byte
		wantFormat  string
	}{
		{name: "png", contentType: "image/png", data: pngBytes, wantFormat: "png"},
		{name: "jpeg", contentType: "image/jpeg", data: jpegBytes, wantFormat: "jpg"},
		{name: "octet stream", contentType: "application/octet-stream", data: pngBytes, wantFormat: "png"},
		{name: "no content type", contentType: "", data: jpegBytes, wantFormat: "jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType, body := systemOneForm(t, multipartRequest, formPart{name: "door-photo", contentType: tt.contentType, data: tt.data})

			got, err := systemOneMultipartToJSON(contentType, body)
			require.NoError(t, err)
			assert.Contains(t, string(got), `"questions":{"zz":`, "the questions keep their order")
			var parsed struct {
				Assets map[string]map[string]string `json:"assets"`
			}
			require.NoError(t, json.Unmarshal(got, &parsed))
			assert.Equal(t, map[string]string{
				"type":        "image",
				"format":      tt.wantFormat,
				"data_base64": base64.StdEncoding.EncodeToString(tt.data),
			}, parsed.Assets["ref-door"])
		})
	}
}

// A form TypeSafe would reject is rejected here too, before any routing.
func TestSystemOneMultipartToJSON_RejectsMalformedForms(t *testing.T) {
	png := formPart{name: "door-photo", contentType: "image/png", data: pngBytes}
	tests := []struct {
		name    string
		request string
		parts   []formPart
		want    string
	}{
		{name: "unused part", request: `{"model":"m","state":"x","questions":{}}`, parts: []formPart{png}, want: "not used by any asset"},
		{name: "missing part", request: multipartRequest, want: "does not hold"},
		{name: "duplicate part", request: multipartRequest, parts: []formPart{png, png}, want: "unique"},
		{name: "shared part", request: `{"model":"m","state":"a b","assets":{"a":{"part":"door-photo"},"b":{"part":"door-photo"}},"questions":{}}`, parts: []formPart{png}, want: "more than one asset"},
		{name: "gif part", request: multipartRequest, parts: []formPart{{name: "door-photo", contentType: "image/gif", data: []byte("GIF89a")}}, want: "PNG or JPEG"},
		{name: "unknown bytes", request: multipartRequest, parts: []formPart{{name: "door-photo", data: []byte("hello")}}, want: "not a PNG or JPEG"},
		{name: "png content type with other bytes", request: multipartRequest, parts: []formPart{{name: "door-photo", contentType: "image/png", data: []byte("GIF89a")}}, want: "not a PNG or JPEG"},
		{name: "stated png with gif bytes", request: `{"model":"m","state":"a","assets":{"a":{"part":"door-photo","format":"png"}},"questions":{}}`, parts: []formPart{{name: "door-photo", data: []byte("GIF89a")}}, want: "not a PNG or JPEG"},
		{name: "request part not JSON", request: `not json`, want: "not a JSON request"},
		{name: "stated gif format", request: `{"model":"m","state":"a","assets":{"a":{"part":"door-photo","format":"gif"}},"questions":{}}`, parts: []formPart{{name: "door-photo", contentType: "image/gif", data: []byte("GIF89a")}}, want: "png, jpg, or jpeg"},
		{name: "format not a string", request: `{"model":"m","state":"a","assets":{"a":{"part":"door-photo","format":1}},"questions":{}}`, parts: []formPart{png}, want: "format must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType, body := systemOneForm(t, tt.request, tt.parts...)
			_, err := systemOneMultipartToJSON(contentType, body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// A multipart request through the endpoint reaches the provider as JSON, with
// the image inline and a JSON content type, so every System One target,
// failovers included, receives a request it can read.
func TestSystemOne_AcceptsMultipartRequests(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	srv := New(provider, &Config{})
	contentType, body := systemOneForm(t, multipartRequest, formPart{name: "door-photo", contentType: "image/png", data: pngBytes})

	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("TypeSafe-Danger-Zone-Images", "true")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	forwarded := forwardedSystemOneBody(t, provider)
	assert.Equal(t, "kev-latest", forwarded["model"])
	assets, ok := forwarded["assets"].(map[string]any)
	require.True(t, ok, "assets = %#v", forwarded["assets"])
	asset, ok := assets["ref-door"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, base64.StdEncoding.EncodeToString(pngBytes), asset["data_base64"])
	assert.NotContains(t, asset, "part")
	assert.Equal(t, "application/json", provider.lastPassthroughReq.Headers.Get("Content-Type"))
	assert.Equal(t, "true", provider.lastPassthroughReq.Headers.Get("TypeSafe-Danger-Zone-Images"))
}

func TestSystemOne_RejectsMalformedMultipartRequests(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	srv := New(provider, &Config{})
	contentType, body := systemOneForm(t, multipartRequest)

	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid multipart request")
	assert.Nil(t, provider.lastPassthroughReq)
}

// A stated format TypeSafe accepts is kept as stated.
func TestSystemOneMultipartToJSON_KeepsAStatedFormat(t *testing.T) {
	contentType, body := systemOneForm(t, `{"model":"m","state":"a","assets":{"a":{"part":"p","format":"jpeg"}},"questions":{}}`,
		formPart{name: "p", contentType: "image/png", data: pngBytes})
	got, err := systemOneMultipartToJSON(contentType, body)
	require.NoError(t, err)
	assert.Contains(t, string(got), `"format":"jpeg"`)
}
