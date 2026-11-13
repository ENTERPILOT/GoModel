package server

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// allowlistAuthorizer stands in for a caller's model allowlist: the listed
// models pass the model check, and the provider check passes only when the
// whole provider is granted.
type allowlistAuthorizer struct {
	recordingModelAuthorizer
	models         []string
	wholeProvider  bool
	modelChecks    int
	providerChecks int
}

func (a *allowlistAuthorizer) ValidateModelAccess(_ context.Context, selector core.ModelSelector) error {
	a.modelChecks++
	if slices.Contains(a.models, selector.Model) {
		return nil
	}
	return core.NewInvalidRequestError("requested model is not available for this API key", nil).WithCode("model_access_denied")
}

func (a *allowlistAuthorizer) ValidateProviderAccess(_ context.Context, provider string) error {
	a.providerChecks++
	if a.wholeProvider {
		return nil
	}
	return core.NewInvalidRequestError("this API key needs all of provider "+provider, nil).WithCode("model_access_denied")
}

func multipartModelBody(t *testing.T, model string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", model))
	require.NoError(t, writer.WriteField("purpose", "batch"))
	require.NoError(t, writer.Close())
	return &body, writer.FormDataContentType()
}

// A model grant covers inference on that model. Every other passthrough
// request acts on the provider account, so it needs the whole provider even
// when its body names a granted model.
func TestProviderPassthroughRoute_ModelGrantCoversOnlyInference(t *testing.T) {
	const jsonBody = `{"model":"gpt-allowed"}`
	multipartBody, multipartType := multipartModelBody(t, "gpt-allowed")

	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
		wantModel   bool // the model grant alone authorizes the request
	}{
		{name: "chat completions", method: http.MethodPost, target: "/p/openai/chat/completions", body: jsonBody, contentType: "application/json", wantModel: true},
		{name: "chat completions through the v1 alias", method: http.MethodPost, target: "/p/openai/v1/chat/completions", body: jsonBody, contentType: "application/json", wantModel: true},
		{name: "responses", method: http.MethodPost, target: "/p/openai/responses", body: jsonBody, contentType: "application/json", wantModel: true},
		{name: "embeddings", method: http.MethodPost, target: "/p/openai/embeddings", body: jsonBody, contentType: "application/json", wantModel: true},
		{name: "anthropic messages", method: http.MethodPost, target: "/p/anthropic/v1/messages", body: jsonBody, contentType: "application/json", wantModel: true},
		{name: "json content type with a suffix", method: http.MethodPost, target: "/p/openai/chat/completions", body: jsonBody, contentType: "application/vnd.api+json; charset=utf-8", wantModel: true},

		{name: "file upload as json", method: http.MethodPost, target: "/p/openai/files", body: jsonBody, contentType: "application/json"},
		{name: "batch creation", method: http.MethodPost, target: "/p/openai/batches", body: jsonBody, contentType: "application/json"},
		{name: "anthropic message batches", method: http.MethodPost, target: "/p/anthropic/v1/messages/batches", body: jsonBody, contentType: "application/json"},
		{name: "stored response sub-resource", method: http.MethodPost, target: "/p/openai/responses/resp_1/cancel", body: jsonBody, contentType: "application/json"},
		{name: "inference path below another resource", method: http.MethodPost, target: "/p/openai/deployments/d1/chat/completions", body: jsonBody, contentType: "application/json"},
		{name: "inference path with a trailing slash", method: http.MethodPost, target: "/p/openai/chat/completions/", body: jsonBody, contentType: "application/json"},
		{name: "inference path in another case", method: http.MethodPost, target: "/p/openai/Chat/Completions", body: jsonBody, contentType: "application/json"},
		{name: "delete with a json body", method: http.MethodDelete, target: "/p/openai/files/file-1", body: jsonBody, contentType: "application/json"},
		{name: "get with a json body", method: http.MethodGet, target: "/p/openai/responses/resp_1", body: jsonBody, contentType: "application/json"},
		{name: "get on an inference path", method: http.MethodGet, target: "/p/openai/chat/completions", body: jsonBody, contentType: "application/json"},
		{name: "put on an inference path", method: http.MethodPut, target: "/p/openai/responses", body: jsonBody, contentType: "application/json"},
		{name: "patch with a json body", method: http.MethodPatch, target: "/p/openai/files/file-1", body: jsonBody, contentType: "application/json"},
		{name: "multipart upload", method: http.MethodPost, target: "/p/openai/files", body: multipartBody.String(), contentType: multipartType},
		{name: "multipart body on an inference path", method: http.MethodPost, target: "/p/openai/chat/completions", body: multipartBody.String(), contentType: multipartType},
		{name: "form body on an inference path", method: http.MethodPost, target: "/p/openai/chat/completions", body: "model=gpt-allowed", contentType: "application/x-www-form-urlencoded"},
		{name: "plain text body on an inference path", method: http.MethodPost, target: "/p/openai/chat/completions", body: "model: gpt-allowed", contentType: "text/plain"},
		{name: "body without a content type", method: http.MethodPost, target: "/p/openai/files", body: jsonBody},
	}
	for _, tt := range tests {
		for _, wholeProvider := range []bool{false, true} {
			name := tt.name + "/model grant"
			if wholeProvider {
				name = tt.name + "/provider grant"
			}
			t.Run(name, func(t *testing.T) {
				mock := &mockProvider{passthroughResponse: okPassthroughResponse()}
				authorizer := &allowlistAuthorizer{models: []string{"gpt-allowed"}, wholeProvider: wholeProvider}
				srv := New(mock, &Config{ModelAuthorizer: authorizer})
				req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
				if tt.contentType != "" {
					req.Header.Set("Content-Type", tt.contentType)
				}
				rec := httptest.NewRecorder()

				srv.ServeHTTP(rec, req)

				if tt.wantModel || wholeProvider {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.NotNil(t, mock.lastPassthroughReq)
				} else {
					require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
					assert.Contains(t, rec.Body.String(), "model_access_denied")
					assert.Nil(t, mock.lastPassthroughReq, "a denied request must not reach the provider")
				}
				if tt.wantModel {
					assert.Equal(t, 1, authorizer.modelChecks)
					assert.Zero(t, authorizer.providerChecks, "inference on a granted model needs no provider grant")
				} else {
					assert.Equal(t, 1, authorizer.providerChecks, "the provider grant must be checked")
				}
			})
		}
	}
}

// A provider-wide grant does not lift a per-model denial: a model the body
// names is still checked wherever the request goes.
func TestProviderPassthroughRoute_NamedModelIsCheckedOnEveryEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		contentType string
	}{
		{name: "inference", method: http.MethodPost, target: "/p/openai/chat/completions", contentType: "application/json"},
		{name: "inference with a json body sent as plain text", method: http.MethodPost, target: "/p/openai/chat/completions", contentType: "text/plain"},
		{name: "other endpoint", method: http.MethodPost, target: "/p/openai/images/generations", contentType: "application/json"},
		{name: "other method", method: http.MethodDelete, target: "/p/openai/files/file-1", contentType: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{passthroughResponse: okPassthroughResponse()}
			authorizer := &allowlistAuthorizer{models: []string{"gpt-allowed"}, wholeProvider: true}
			srv := New(mock, &Config{ModelAuthorizer: authorizer})
			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(`{"model":"gpt-other"}`))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, 1, authorizer.modelChecks)
			assert.Zero(t, authorizer.providerChecks)
			assert.Nil(t, mock.lastPassthroughReq, "a denied request must not reach the provider")
		})
	}
}

func TestPassthroughRunsModelInference(t *testing.T) {
	snapshot := func(method string) context.Context {
		return core.WithRequestSnapshot(context.Background(), &core.RequestSnapshot{Method: method})
	}
	tests := []struct {
		name string
		ctx  context.Context
		info *core.PassthroughRouteInfo
		want bool
	}{
		{name: "post to an inference endpoint", ctx: snapshot(http.MethodPost), info: &core.PassthroughRouteInfo{NormalizedEndpoint: "chat/completions"}, want: true},
		{name: "post elsewhere", ctx: snapshot(http.MethodPost), info: &core.PassthroughRouteInfo{NormalizedEndpoint: "files"}},
		{name: "other method", ctx: snapshot(http.MethodGet), info: &core.PassthroughRouteInfo{NormalizedEndpoint: "chat/completions"}},
		{name: "no snapshot", ctx: context.Background(), info: &core.PassthroughRouteInfo{NormalizedEndpoint: "chat/completions"}},
		{name: "no route info", ctx: snapshot(http.MethodPost)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, passthroughRunsModelInference(tt.ctx, tt.info))
		})
	}
}
