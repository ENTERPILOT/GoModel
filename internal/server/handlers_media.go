package server

import (
	"github.com/labstack/echo/v5"
)

// AudioSpeech handles POST /v1/audio/speech.
//
// @Summary      Create speech (text-to-speech)
// @Tags         audio
// @Accept       json
// @Produce      application/octet-stream
// @Security     BearerAuth
// @Param        request  body      core.AudioSpeechRequest  true  "Text-to-speech request"
// @Success      200      {file}    file  "Binary audio in the requested response_format"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/speech [post]
func (h *Handler) AudioSpeech(c *echo.Context) error {
	return h.audio().CreateSpeech(c)
}

// AudioTranscriptions handles POST /v1/audio/transcriptions.
//
// @Summary      Create transcription (speech-to-text)
// @Tags         audio
// @Accept       mpfd
// @Produce      json
// @Produce      plain
// @Security     BearerAuth
// @Param        file             formData  file    true   "Audio file to transcribe"
// @Param        model            formData  string  true   "Model ID"
// @Param        language         formData  string  false  "Input language (ISO-639-1)"
// @Param        prompt           formData  string  false  "Optional text to guide the model"
// @Param        response_format          formData  string    false  "json, text, srt, verbose_json, or vtt"
// @Param        temperature              formData  number    false  "Sampling temperature (0-1)"
// @Param        timestamp_granularities[] formData  []string  false  "Timestamp granularities to populate: word and/or segment"
// @Success      200                      {object}  map[string]interface{}  "Transcription in the requested response_format: a JSON object for json/verbose_json, or a text/plain body for text/srt/vtt"
// @Failure      400              {object}  core.OpenAIErrorEnvelope
// @Failure      401              {object}  core.OpenAIErrorEnvelope
// @Failure      404              {object}  core.OpenAIErrorEnvelope
// @Failure      502              {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/transcriptions [post]
func (h *Handler) AudioTranscriptions(c *echo.Context) error {
	return h.audio().CreateTranscription(c)
}

// ImageGenerations handles POST /v1/images/generations.
//
// @Summary      Create image (image generation)
// @Tags         images
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ImageGenerationRequest  true  "Image generation request"
// @Success      200      {object}  core.ImageGenerationResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/images/generations [post]
func (h *Handler) ImageGenerations(c *echo.Context) error {
	return h.images().CreateImage(c)
}

// ImageEdits handles POST /v1/images/edits.
//
// @Summary      Create image edit (inpainting / image-to-image)
// @Tags         images
// @Accept       mpfd
// @Produce      json
// @Security     BearerAuth
// @Param        image            formData  file    false  "Source image to edit (single-image form; gpt-image-1 and DALL·E 2). At least one of image or image[] is required"
// @Param        image[]          formData  file    false  "Repeatable field carrying several source images (gpt-image-1, up to 16). At least one of image or image[] is required"
// @Param        prompt           formData  string  true   "Text description of the desired edit"
// @Param        model            formData  string  true   "Model ID"
// @Param        mask             formData  file    false  "PNG whose transparent areas mark where the image should be edited"
// @Param        n                formData  integer false  "Number of images to generate"  minimum(1)
// @Param        size             formData  string  false  "Output size, e.g. 1024x1024"
// @Param        quality          formData  string  false  "Output quality (model-specific)"
// @Param        response_format  formData  string  false  "url or b64_json (DALL·E 2 only; gpt-image-1 always returns b64_json)"
// @Param        user             formData  string  false  "End-user identifier forwarded to the provider"
// @Success      200      {object}  core.ImageGenerationResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/images/edits [post]
func (h *Handler) ImageEdits(c *echo.Context) error {
	return h.images().CreateImageEdit(c)
}

// AudioTranslations handles POST /v1/audio/translations.
//
// @Summary      Translate audio into English
// @Tags         audio
// @Accept       mpfd
// @Produce      json
// @Produce      plain
// @Security     BearerAuth
// @Param        file             formData  file    true   "Audio file to translate"
// @Param        model            formData  string  true   "Model ID"
// @Param        prompt           formData  string  false  "Optional English text to guide the model"
// @Param        response_format  formData  string  false  "json, text, srt, verbose_json, or vtt"
// @Param        temperature      formData  number  false  "Sampling temperature (0-1)"
// @Success      200              {object}  map[string]interface{}  "English translation in the requested response_format"
// @Failure      400              {object}  core.OpenAIErrorEnvelope
// @Failure      401              {object}  core.OpenAIErrorEnvelope
// @Failure      404              {object}  core.OpenAIErrorEnvelope
// @Failure      502              {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/translations [post]
func (h *Handler) AudioTranslations(c *echo.Context) error {
	return h.audio().CreateTranslation(c)
}
