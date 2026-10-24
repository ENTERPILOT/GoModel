// Package systemone answers OpenAI Decisions requests with TypeSafe's System
// One API, which asks the same three kinds of question under other names: a
// predicate is a noul, and choice and score keep their names. It serves every
// provider whose upstream speaks System One: jev (hosted Jev and self-hosted
// Kev servers) and OpenRouter, which serves Jev and Kev natively.
package systemone

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// maxSystemOneAnswerBytes caps the System One answer read for translation. A
// request holds at most a few hundred questions, so a real answer is far
// smaller; the cap keeps a misbehaving upstream from being buffered whole.
const maxSystemOneAnswerBytes = 8 << 20

// Sender sends one System One request upstream: a provider's Passthrough.
type Sender func(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error)

// Options describe the provider a Decisions request is answered for.
type Options struct {
	// ProviderType names the provider in errors.
	ProviderType string
	// ImagesRefusal, when set, refuses a request with images with this
	// message: the upstream reads text only.
	ImagesRefusal string
}

// AnswerDecisions answers an OpenAI Decisions request with System One. The
// request is translated to a System One request, sent to /v1/systemone
// through send, and the answer translated back, so the caller sees OpenAI's
// shapes. Members System One has no place for (prompt caching,
// safety_identifier) are dropped. Images become System One image assets,
// which TypeSafe serves only with the TypeSafe-Danger-Zone-Images header;
// the gateway does not fetch images, so they must arrive inline as base64
// data URLs.
func AnswerDecisions(ctx context.Context, req *core.PassthroughRequest, send Sender, opts Options) (*core.PassthroughResponse, error) {
	if req == nil || req.Body == nil {
		return nil, core.NewInvalidRequestError("decisions request is required", nil)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	var decision decisionRequest
	if err := json.Unmarshal(raw, &decision); err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	plan, err := planSystemOne(&decision)
	if err != nil {
		return nil, err
	}
	images := len(plan.request.Assets) > 0
	if images && opts.ImagesRefusal != "" {
		return nil, core.NewInvalidRequestError(opts.ImagesRefusal, nil).WithParam("input")
	}
	body, err := json.Marshal(plan.request)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode System One request", err)
	}

	resp, err := send(ctx, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     "systemone",
		Operation:    req.Operation,
		Model:        req.Model,
		Body:         io.NopCloser(bytes.NewReader(body)),
		Headers:      systemOneHeaders(req.Headers, images),
		ProviderName: req.ProviderName,
	})
	if err != nil || resp == nil || resp.StatusCode >= http.StatusBadRequest {
		// Errors are relayed as the upstream wrote them; the gateway parses
		// them like any provider error.
		return resp, err
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxSystemOneAnswerBytes+1))
	if err != nil {
		return nil, core.NewProviderError(opts.ProviderType, http.StatusBadGateway, "failed to read System One answer", err)
	}
	if len(answer) > maxSystemOneAnswerBytes {
		return nil, core.NewProviderError(opts.ProviderType, http.StatusBadGateway, "System One answer is too large to translate", nil)
	}
	translated, err := plan.decision(answer)
	if err != nil {
		return nil, core.NewProviderError(opts.ProviderType, http.StatusBadGateway, "failed to translate System One answer: "+err.Error(), err)
	}

	// The body is rewritten, so the upstream's length and encoding no longer
	// describe it.
	headers := http.Header(providers.CloneHTTPHeaders(resp.Headers))
	headers.Del("Content-Length")
	headers.Del("Content-Encoding")
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       io.NopCloser(bytes.NewReader(translated)),
	}, nil
}
