package server

import (
	"github.com/labstack/echo/v5"
)

// Batches handles POST /v1/batches.
//
// OpenAI-compatible fields are accepted (`input_file_id`, `endpoint`, `completion_window`, `metadata`).
// Inline `requests` are also accepted for providers with native inline batch support (for example Anthropic).
//
// @Summary      Create a native provider batch
// @Tags         batch
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.BatchRequest  true  "Batch request"
// @Success      200      {object}  core.BatchResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches [post]
func (h *Handler) Batches(c *echo.Context) error {
	return h.nativeBatch().Batches(c)
}

// GetBatch handles GET /v1/batches/{id}.
//
// @Summary      Get a batch
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      404  {object}  core.OpenAIErrorEnvelope
// @Failure      500  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches/{id} [get]
func (h *Handler) GetBatch(c *echo.Context) error {
	return h.nativeBatch().GetBatch(c)
}

// ListBatches handles GET /v1/batches.
//
// @Summary      List batches
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        after  query     string  false  "Pagination cursor"
// @Param        limit  query     int     false  "Maximum items to return (1-100, default 20)"
// @Success      200    {object}  core.BatchListResponse
// @Failure      400    {object}  core.OpenAIErrorEnvelope
// @Failure      401    {object}  core.OpenAIErrorEnvelope
// @Failure      404    {object}  core.OpenAIErrorEnvelope
// @Failure      500    {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches [get]
func (h *Handler) ListBatches(c *echo.Context) error {
	return h.nativeBatch().ListBatches(c)
}

// CancelBatch handles POST /v1/batches/{id}/cancel.
//
// @Summary      Cancel a batch
// @Tags         batch
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      404  {object}  core.OpenAIErrorEnvelope
// @Failure      500  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches/{id}/cancel [post]
func (h *Handler) CancelBatch(c *echo.Context) error {
	return h.nativeBatch().CancelBatch(c)
}

// BatchResults handles GET /v1/batches/{id}/results.
//
// @Summary      Get batch results
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResultsResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      404  {object}  core.OpenAIErrorEnvelope
// @Failure      409  {object}  core.OpenAIErrorEnvelope
// @Failure      500  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches/{id}/results [get]
func (h *Handler) BatchResults(c *echo.Context) error {
	return h.nativeBatch().BatchResults(c)
}

// CreateFile handles POST /v1/files.
//
// @Summary      Upload a file
// @Tags         files
// @Accept       mpfd
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Provider override when multiple providers are configured"
// @Param        purpose   formData  string  true   "File purpose"
// @Param        file      formData  file    true   "File to upload"
// @Success      200       {object}  core.FileObject
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files [post]
func (h *Handler) CreateFile(c *echo.Context) error {
	return h.nativeFiles().CreateFile(c)
}

// ListFiles handles GET /v1/files.
//
// @Summary      List files
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Provider filter"
// @Param        purpose   query     string  false  "File purpose filter"
// @Param        after     query     string  false  "Pagination cursor"
// @Param        limit     query     int     false  "Maximum items to return (1-100, default 20)"
// @Success      200       {object}  core.FileListResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files [get]
func (h *Handler) ListFiles(c *echo.Context) error {
	return h.nativeFiles().ListFiles(c)
}

// GetFile handles GET /v1/files/{id}.
//
// @Summary      Get file metadata
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "File ID"
// @Param        provider  query     string  false  "Provider override"
// @Success      200       {object}  core.FileObject
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files/{id} [get]
func (h *Handler) GetFile(c *echo.Context) error {
	return h.nativeFiles().GetFile(c)
}

// DeleteFile handles DELETE /v1/files/{id}.
//
// @Summary      Delete a file
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "File ID"
// @Param        provider  query     string  false  "Provider override"
// @Success      200       {object}  core.FileDeleteResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files/{id} [delete]
func (h *Handler) DeleteFile(c *echo.Context) error {
	return h.nativeFiles().DeleteFile(c)
}

// GetFileContent handles GET /v1/files/{id}/content.
//
// @Summary      Download file content
// @Tags         files
// @Produce      application/octet-stream
// @Security     BearerAuth
// @Param        id        path   string  true   "File ID"
// @Param        provider  query  string  false  "Provider override"
// @Success      200       {file}  file  "Raw file content"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files/{id}/content [get]
func (h *Handler) GetFileContent(c *echo.Context) error {
	return h.nativeFiles().GetFileContent(c)
}
