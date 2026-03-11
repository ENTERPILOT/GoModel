package core

import "strings"

const (
	// FileActionCreate represents POST /v1/files.
	FileActionCreate = "create"
	// FileActionList represents GET /v1/files.
	FileActionList = "list"
	// FileActionGet represents GET /v1/files/{id}.
	FileActionGet = "get"
	// FileActionDelete represents DELETE /v1/files/{id}.
	FileActionDelete = "delete"
	// FileActionContent represents GET /v1/files/{id}/content.
	FileActionContent = "content"
)

// FileCreateRequest represents an OpenAI-compatible file upload request.
// The actual request is multipart/form-data; Content is not serialized.
type FileCreateRequest struct {
	Purpose  string `json:"purpose"`
	Filename string `json:"filename,omitempty"`
	Content  []byte `json:"-"`
}

// FileRequestSemantic is the sparse canonical metadata the gateway can derive for /v1/files* routes.
// It intentionally excludes file bytes, which remain transport data rather than semantic data.
type FileRequestSemantic struct {
	Action   string
	Provider string
	Purpose  string
	Filename string
	FileID   string
	After    string
	LimitRaw string
	Limit    int
	HasLimit bool
}

func (req *FileRequestSemantic) ensureParsedLimit() error {
	if req == nil || req.LimitRaw == "" || req.HasLimit {
		return nil
	}
	parsed, err := parseRouteLimit(req.LimitRaw)
	if err != nil {
		return err
	}
	req.Limit = parsed
	req.HasLimit = true
	return nil
}

// FileMultipartMetadataReader exposes the small subset of multipart form data
// needed for sparse file-create semantics.
type FileMultipartMetadataReader interface {
	Value(name string) string
	Filename(name string) (string, bool)
}

func EnrichFileCreateRequestSemantic(req *FileRequestSemantic, reader FileMultipartMetadataReader) *FileRequestSemantic {
	if req == nil || req.Action != FileActionCreate || reader == nil {
		return req
	}

	if req.Provider == "" {
		req.Provider = strings.TrimSpace(reader.Value("provider"))
	}
	if req.Purpose == "" {
		req.Purpose = strings.TrimSpace(reader.Value("purpose"))
	}
	if req.Filename == "" {
		if filename, ok := reader.Filename("file"); ok {
			req.Filename = strings.TrimSpace(filename)
		}
	}
	return req
}

// FileObject represents an OpenAI-compatible file object.
type FileObject struct {
	ID            string  `json:"id"`
	Object        string  `json:"object"`
	Bytes         int64   `json:"bytes"`
	CreatedAt     int64   `json:"created_at"`
	Filename      string  `json:"filename"`
	Purpose       string  `json:"purpose"`
	Status        string  `json:"status,omitempty"`
	StatusDetails *string `json:"status_details,omitempty"`

	// Gateway enrichment for multi-provider deployments.
	Provider string `json:"provider,omitempty"`
}

// FileListResponse is returned by GET /v1/files.
type FileListResponse struct {
	Object  string       `json:"object"`
	Data    []FileObject `json:"data"`
	HasMore bool         `json:"has_more,omitempty"`
}

// FileDeleteResponse is returned by DELETE /v1/files/{id}.
type FileDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// FileContentResponse wraps raw file bytes with response metadata.
type FileContentResponse struct {
	ID          string
	Filename    string
	ContentType string
	Data        []byte
}
