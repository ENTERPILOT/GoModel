package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func validatedOpenAICompatibleFileID(client *llmclient.Client, id string) (string, error) {
	if client == nil {
		return "", core.NewProviderError("openai_compatible", http.StatusBadGateway, "provider client is not configured", nil)
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", core.NewInvalidRequestError("file id is required", nil)
	}
	return trimmed, nil
}

func doOpenAICompatibleFileIDRequest[T any](ctx context.Context, client *llmclient.Client, method, id string, defaultObject string) (*T, error) {
	trimmedID, err := validatedOpenAICompatibleFileID(client, id)
	if err != nil {
		return nil, err
	}

	var resp T
	if err := client.Do(ctx, llmclient.Request{
		Method:   method,
		Endpoint: "/files/" + url.PathEscape(trimmedID),
	}, &resp); err != nil {
		return nil, err
	}
	switch typed := any(&resp).(type) {
	case *core.FileObject:
		typed.ID = trimmedID
		if typed.Object == "" {
			typed.Object = defaultObject
		}
	case *core.FileDeleteResponse:
		typed.ID = trimmedID
		if typed.Object == "" {
			typed.Object = defaultObject
		}
	}
	return &resp, nil
}

// CreateOpenAICompatibleFile uploads a file using the OpenAI-compatible multipart files API.
func CreateOpenAICompatibleFile(ctx context.Context, client *llmclient.Client, req *core.FileCreateRequest) (*core.FileObject, error) {
	if client == nil {
		return nil, core.NewInvalidRequestError("provider client is not configured", nil)
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("request is required", nil)
	}
	if strings.TrimSpace(req.Purpose) == "" {
		return nil, core.NewInvalidRequestError("purpose is required", nil)
	}
	content := req.ContentReader
	if content == nil && len(req.Content) > 0 {
		content = bytes.NewReader(req.Content)
	}
	if content == nil {
		return nil, core.NewInvalidRequestError("file is required", nil)
	}

	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = "upload.jsonl"
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer func() {
			_ = pw.Close()
		}()
		if err := writer.WriteField("purpose", strings.TrimSpace(req.Purpose)); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to write purpose field", err))
			return
		}
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to create multipart file field", err))
			return
		}
		if _, err := io.Copy(part, content); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to stream file content", err))
			return
		}
		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to finalize multipart payload", err))
			return
		}
	}()

	var fileObj core.FileObject
	if err := client.Do(ctx, llmclient.Request{
		Method:        http.MethodPost,
		Endpoint:      "/files",
		RawBodyReader: pr,
		Headers: http.Header{
			"Content-Type": {writer.FormDataContentType()},
		},
	}, &fileObj); err != nil {
		return nil, err
	}
	if fileObj.Object == "" {
		fileObj.Object = "file"
	}
	return &fileObj, nil
}

// ListOpenAICompatibleFiles lists files using OpenAI-compatible files API.
func ListOpenAICompatibleFiles(ctx context.Context, client *llmclient.Client, purpose string, limit int, after string) (*core.FileListResponse, error) {
	if client == nil {
		return nil, core.NewInvalidRequestError("provider client is not configured", nil)
	}

	values := url.Values{}
	if trimmed := strings.TrimSpace(purpose); trimmed != "" {
		values.Set("purpose", trimmed)
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if trimmed := strings.TrimSpace(after); trimmed != "" {
		values.Set("after", trimmed)
	}

	endpoint := "/files"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp core.FileListResponse
	if err := client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp); err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "list"
	}
	return &resp, nil
}

// GetOpenAICompatibleFile retrieves a file object by id after normalizing the
// incoming id via validatedOpenAICompatibleFileID. Missing response ID and
// Object fields are synthesized on the returned file object.
func GetOpenAICompatibleFile(ctx context.Context, client *llmclient.Client, id string) (*core.FileObject, error) {
	return doOpenAICompatibleFileIDRequest[core.FileObject](ctx, client, http.MethodGet, id, "file")
}

// DeleteOpenAICompatibleFile deletes a file object by id after normalizing the
// incoming id via validatedOpenAICompatibleFileID. Missing response ID and
// Object fields are synthesized on the returned delete response.
func DeleteOpenAICompatibleFile(ctx context.Context, client *llmclient.Client, id string) (*core.FileDeleteResponse, error) {
	return doOpenAICompatibleFileIDRequest[core.FileDeleteResponse](ctx, client, http.MethodDelete, id, "file")
}

// GetOpenAICompatibleFileContent fetches file bytes via /files/{id}/content
// after normalizing the incoming id via validatedOpenAICompatibleFileID. The
// returned response always includes the normalized file ID.
func GetOpenAICompatibleFileContent(ctx context.Context, client *llmclient.Client, id string) (*core.FileContentResponse, error) {
	trimmedID, err := validatedOpenAICompatibleFileID(client, id)
	if err != nil {
		return nil, err
	}

	raw, err := client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/files/" + url.PathEscape(trimmedID) + "/content",
	})
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, core.NewProviderError("openai_compatible", http.StatusBadGateway, "provider returned empty file content response", fmt.Errorf("nil response"))
	}

	return &core.FileContentResponse{
		ID:          trimmedID,
		ContentType: "application/octet-stream",
		Data:        raw.Body,
	}, nil
}
