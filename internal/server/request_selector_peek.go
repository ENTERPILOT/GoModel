package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const requestSelectorPeekLimit int64 = 64 * 1024

type requestBodySelectorHints struct {
	model        string
	provider     string
	stream       bool
	streamParsed bool
	parsed       bool
	complete     bool
}

func seedRequestBodySelectorHints(req *http.Request, bodyMode core.BodyMode, env *core.WhiteBoxPrompt) {
	if !shouldPeekRequestBodySelectors(req, bodyMode, env) {
		return
	}

	hints := peekRequestBodySelectorHints(req, requestSelectorPeekLimit)
	if bodyMode == core.BodyModeOpaque && !hints.complete {
		// A partial selector must not become authoritative because a duplicate
		// field beyond the peek boundary could change what the provider executes.
		if hints.model != "" {
			if completeHints := peekCompleteRequestBodySelectorHints(req, requestSelectorPeekLimit); completeHints.complete {
				hints = completeHints
			}
		}
		if hints.complete {
			core.ApplyBodySelectorHints(env, hints.model, hints.provider, hints.stream)
		} else if hints.streamParsed {
			core.ApplyBodyStreamHint(env, hints.stream)
		}
	} else if hints.parsed || hints.streamParsed {
		core.ApplyBodySelectorHints(env, hints.model, hints.provider, hints.stream)
	}
	if !hints.streamParsed {
		core.MarkPassthroughStreamUncertain(env)
	}
}

func shouldPeekRequestBodySelectors(req *http.Request, bodyMode core.BodyMode, env *core.WhiteBoxPrompt) bool {
	if req == nil || req.Body == nil || env == nil {
		return false
	}
	switch bodyMode {
	case core.BodyModeJSON:
		return true
	case core.BodyModeOpaque:
		return contentTypeLooksJSON(req.Header.Get("Content-Type"))
	default:
		return false
	}
}

func contentTypeLooksJSON(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return strings.Contains(contentType, "json")
}

func peekRequestBodySelectorHints(req *http.Request, limit int64) requestBodySelectorHints {
	if req == nil || req.Body == nil || limit <= 0 {
		return requestBodySelectorHints{}
	}

	originalBody := req.Body
	var consumed bytes.Buffer
	limited := io.LimitReader(originalBody, limit)
	hints := decodeRequestBodySelectorHints(io.TeeReader(limited, &consumed))

	req.Body = &combinedReadCloser{
		Reader: io.MultiReader(bytes.NewReader(consumed.Bytes()), originalBody),
		rc:     originalBody,
	}
	return hints
}

// peekCompleteRequestBodySelectorHints returns hints only when the entire body
// fits within limit and has no duplicate selector fields. The body is restored
// before returning so passthrough forwarding remains byte-for-byte unchanged.
func peekCompleteRequestBodySelectorHints(req *http.Request, limit int64) requestBodySelectorHints {
	if req == nil || req.Body == nil || limit <= 0 || req.ContentLength > limit {
		return requestBodySelectorHints{}
	}

	originalBody := req.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, limit+1))
	req.Body = &combinedReadCloser{
		Reader: io.MultiReader(bytes.NewReader(body), originalBody),
		rc:     originalBody,
	}
	if err != nil || int64(len(body)) > limit {
		return requestBodySelectorHints{}
	}
	return decodeCompleteRequestBodySelectorHints(bytes.NewReader(body))
}

func decodeRequestBodySelectorHints(r io.Reader) requestBodySelectorHints {
	return decodeRequestBodySelectorHintsWithMode(r, false)
}

func decodeCompleteRequestBodySelectorHints(r io.Reader) requestBodySelectorHints {
	return decodeRequestBodySelectorHintsWithMode(r, true)
}

func decodeRequestBodySelectorHintsWithMode(r io.Reader, requireComplete bool) requestBodySelectorHints {
	dec := json.NewDecoder(r)
	token, err := dec.Token()
	if err != nil {
		return requestBodySelectorHints{}
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return requestBodySelectorHints{}
	}

	var hints requestBodySelectorHints
	var modelSeen, providerSeen, streamSeen bool
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return requestBodySelectorHints{}
		}
		key, ok := keyToken.(string)
		if !ok {
			return requestBodySelectorHints{}
		}

		switch key {
		case "model":
			if requireComplete && modelSeen {
				return requestBodySelectorHints{}
			}
			modelSeen = true
			model, ok, err := readOptionalJSONString(dec)
			if err != nil || !ok {
				return requestBodySelectorHints{}
			}
			hints.model = model
			if !requireComplete && model != "" && hints.provider != "" {
				hints.parsed = true
				return hints
			}
			if !requireComplete && model != "" {
				return hints
			}
		case "provider":
			if requireComplete && providerSeen {
				return requestBodySelectorHints{}
			}
			providerSeen = true
			provider, ok, err := readOptionalJSONString(dec)
			if err != nil || !ok {
				return requestBodySelectorHints{}
			}
			hints.provider = provider
			if !requireComplete && hints.provider != "" && hints.model != "" {
				hints.parsed = true
				return hints
			}
		case "stream":
			if requireComplete && streamSeen {
				return requestBodySelectorHints{}
			}
			streamSeen = true
			stream, ok, err := readOptionalJSONBool(dec)
			if err != nil || !ok {
				return requestBodySelectorHints{}
			}
			hints.stream = stream
			hints.streamParsed = true
		default:
			if err := skipJSONValue(dec); err != nil {
				return requestBodySelectorHints{}
			}
		}
	}
	if requireComplete {
		closing, err := dec.Token()
		if err != nil || closing != json.Delim('}') {
			return requestBodySelectorHints{}
		}
		if _, err := dec.Token(); err != io.EOF {
			return requestBodySelectorHints{}
		}
	}

	hints.parsed = true
	hints.complete = true
	return hints
}

func readOptionalJSONString(dec *json.Decoder) (string, bool, error) {
	token, err := dec.Token()
	if err != nil {
		return "", false, err
	}
	switch value := token.(type) {
	case string:
		return value, true, nil
	case nil:
		return "", true, nil
	default:
		return "", false, nil
	}
}

func readOptionalJSONBool(dec *json.Decoder) (bool, bool, error) {
	token, err := dec.Token()
	if err != nil {
		return false, false, err
	}
	switch value := token.(type) {
	case bool:
		return value, true, nil
	case nil:
		return false, true, nil
	default:
		return false, false, nil
	}
}

func skipJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{', '[':
		depth := 1
		for depth > 0 {
			token, err = dec.Token()
			if err != nil {
				return err
			}
			nested, ok := token.(json.Delim)
			if !ok {
				continue
			}
			switch nested {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
