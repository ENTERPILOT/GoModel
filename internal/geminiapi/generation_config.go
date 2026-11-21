package geminiapi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// applyGenerationConfig carries the generation options that have a portable
// equivalent. topK is deliberately not carried: it is no OpenAI parameter,
// and OpenAI-family providers reject unknown request fields (the same
// decision ADR-0007 records for the Anthropic dialect). Options the type
// does not declare (logprobs, mediaResolution, …) are ignored likewise.
func applyGenerationConfig(chat *core.ChatRequest, cfg *GenerationConfig) error {
	if cfg == nil {
		return nil
	}
	if count := cfg.candidateCount(); count != nil && *count > 1 {
		return unsupported("generationConfig.candidateCount above 1", "generationConfig.candidateCount")
	}
	for _, modality := range cfg.responseModalities() {
		if !strings.EqualFold(strings.TrimSpace(modality), "TEXT") {
			return unsupported("generationConfig.responseModalities other than TEXT", "generationConfig.responseModalities")
		}
	}

	chat.Temperature = cfg.Temperature
	chat.TopP = cfg.topP()
	chat.MaxTokens = cfg.maxOutputTokens()
	chat.Reasoning = thinkingToReasoning(cfg.thinkingConfig())

	fields := map[string]json.RawMessage{}
	if stop := cfg.stopSequences(); len(stop) > 0 {
		if raw, err := json.Marshal(stop); err == nil {
			fields["stop"] = raw
		}
	}
	if cfg.Seed != nil {
		fields["seed"] = json.RawMessage(strconv.FormatInt(*cfg.Seed, 10))
	}
	for name, penalty := range map[string]*float64{
		"presence_penalty":  cfg.presencePenalty(),
		"frequency_penalty": cfg.frequencyPenalty(),
	} {
		if penalty != nil {
			fields[name] = json.RawMessage(strconv.FormatFloat(*penalty, 'f', -1, 64))
		}
	}
	format, err := responseFormat(cfg)
	if err != nil {
		return err
	}
	if format != nil {
		fields["response_format"] = format
	}
	if len(fields) > 0 {
		chat.ExtraFields = core.UnknownJSONFieldsFromMap(fields)
	}
	return nil
}

// responseFormat maps responseMimeType and the response schema onto the
// OpenAI response_format. The schema is sent non-strict: Gemini schemas rarely
// meet OpenAI's strict-mode rules.
func responseFormat(cfg *GenerationConfig) (json.RawMessage, error) {
	var schema any
	if raw := firstRaw(cfg.responseJSONSchema()); raw != nil {
		if firstRaw(cfg.responseSchema()) != nil {
			return nil, core.NewInvalidRequestError("generationConfig.responseSchema and responseJsonSchema are mutually exclusive", nil).WithParam("generationConfig.responseJsonSchema")
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, core.NewInvalidRequestError("generationConfig.responseJsonSchema: "+err.Error(), err).WithParam("generationConfig.responseJsonSchema")
		}
	} else if raw := firstRaw(cfg.responseSchema()); raw != nil {
		converted, err := jsonSchemaFromGemini(raw)
		if err != nil {
			return nil, core.NewInvalidRequestError("generationConfig.responseSchema: "+err.Error(), err).WithParam("generationConfig.responseSchema")
		}
		schema = converted
	}

	switch mimeType := strings.ToLower(strings.TrimSpace(cfg.responseMimeType())); mimeType {
	case "", "text/plain":
		if schema == nil {
			return nil, nil
		}
	case "application/json":
	default:
		return nil, unsupported(fmt.Sprintf("generationConfig.responseMimeType %q", mimeType), "generationConfig.responseMimeType")
	}
	if schema == nil {
		return json.RawMessage(`{"type":"json_object"}`), nil
	}
	return json.Marshal(map[string]any{
		"type":        "json_schema",
		"json_schema": map[string]any{"name": "response", "schema": schema},
	})
}

// thinkingToReasoning maps thinkingConfig onto the canonical reasoning
// effort. A level maps by name; a budget maps by the same thresholds the
// Gemini provider uses the other way round (1024 low, 8192 medium, above
// that high), and 0 turns reasoning off. A dynamic budget (-1) and an empty
// config leave the model's default.
func thinkingToReasoning(cfg *ThinkingConfig) *core.Reasoning {
	if cfg == nil {
		return nil
	}
	switch level := strings.ToLower(strings.TrimSpace(cfg.thinkingLevel())); level {
	case "minimal", "low", "medium", "high":
		return &core.Reasoning{Effort: level}
	}
	budget := cfg.thinkingBudget()
	if budget == nil || *budget < 0 {
		return nil
	}
	switch {
	case *budget == 0:
		return &core.Reasoning{Effort: "none"}
	case *budget <= 1024:
		return &core.Reasoning{Effort: "low"}
	case *budget <= 8192:
		return &core.Reasoning{Effort: "medium"}
	}
	return &core.Reasoning{Effort: "high"}
}
