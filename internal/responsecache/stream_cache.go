package responsecache

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

var (
	cacheLFEventBoundary   = []byte("\n\n")
	cacheCRLFEventBoundary = []byte("\r\n\r\n")
	cacheDataPrefix        = []byte("data:")
	cacheDonePayload       = []byte("[DONE]")
)

type streamResponseDefaults struct {
	Model    string
	Provider string
}

type chatToolCallState struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

type chatChoiceState struct {
	Index        int
	Role         string
	Content      strings.Builder
	Reasoning    strings.Builder
	FinishReason string
	Logprobs     json.RawMessage
	HasLogprobs  bool
	ToolCalls    map[int]*chatToolCallState
}

type chatStreamCacheBuilder struct {
	defaults          streamResponseDefaults
	seen              bool
	ID                string
	Model             string
	Provider          string
	Object            string
	SystemFingerprint string
	Created           int64
	Usage             map[string]any
	Choices           map[int]*chatChoiceState
}

type responsesOutputState struct {
	Index int
	Item  map[string]any

	TextParts      map[int]*strings.Builder
	ReasoningParts map[int]*strings.Builder
	Arguments      strings.Builder
	HasArgs        bool
}

type responsesStreamCacheBuilder struct {
	defaults       streamResponseDefaults
	seen           bool
	Response       map[string]any
	ID             string
	Object         string
	Model          string
	Provider       string
	Status         string
	CreatedAt      int64
	Usage          map[string]any
	Error          map[string]any
	Output         map[int]*responsesOutputState
	ItemIDs        map[string]int
	AssistantIndex int
	HasAssistant   bool
	ReasoningIndex int
	HasReasoning   bool
}

func cacheKeyRequestBody(path string, body []byte) []byte {
	switch path {
	case "/v1/chat/completions":
		req, err := core.DecodeChatRequest(body, nil)
		if err != nil || req == nil {
			return body
		}
		if req.Stream {
			req.StreamOptions = normalizeStreamOptionsForCache(req.StreamOptions)
		} else {
			req.StreamOptions = nil
		}
		normalized, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return normalized
	case "/v1/responses":
		req, err := core.DecodeResponsesRequest(body, nil)
		if err != nil || req == nil {
			return body
		}
		if req.Stream {
			req.StreamOptions = normalizeStreamOptionsForCache(req.StreamOptions)
		} else {
			req.StreamOptions = nil
		}
		normalized, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return normalized
	default:
		return body
	}
}

func isEventStreamContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "text/event-stream"
}

func writeCachedResponse(c *echo.Context, path string, requestBody, cached []byte, cacheType string) error {
	cacheHeader := cacheHeaderValue(cacheType)
	if isStreamingRequest(path, requestBody) {
		auditlog.EnrichEntryWithStream(c, true)
		auditlog.EnrichEntryWithCachedStreamResponse(c, path, cached)
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().Header().Set("Connection", "keep-alive")
		c.Response().Header().Set("X-Cache", cacheHeader)
		c.Response().WriteHeader(http.StatusOK)
		_, _ = c.Response().Write(cached)
		return nil
	}

	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Set("X-Cache", cacheHeader)
	c.Response().WriteHeader(http.StatusOK)
	_, _ = c.Response().Write(cached)
	return nil
}

func cacheHeaderValue(cacheType string) string {
	switch cacheType {
	case CacheTypeExact:
		return CacheHeaderExact
	case CacheTypeSemantic:
		return CacheHeaderSemantic
	default:
		return "HIT (" + cacheType + ")"
	}
}

func renderCachedChatStream(requestBody, cached []byte) ([]byte, error) {
	var resp core.ChatResponse
	if err := json.Unmarshal(cached, &resp); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	includeUsage := streamIncludeUsageRequested("/v1/chat/completions", requestBody)
	usage := chatUsageMap(resp.Usage)
	if !includeUsage {
		usage = nil
	}
	for _, choice := range resp.Choices {
		delta := map[string]any{}
		role := strings.TrimSpace(choice.Message.Role)
		if role == "" {
			role = "assistant"
		}
		delta["role"] = role

		if content := core.ExtractTextContent(choice.Message.Content); content != "" {
			delta["content"] = content
		}
		if reasoning := chatReasoningContent(choice.Message); reasoning != "" {
			delta["reasoning_content"] = reasoning
		}
		if len(choice.Message.ToolCalls) > 0 {
			delta["tool_calls"] = renderChatToolCalls(choice.Message.ToolCalls)
		}

		renderedChoice := map[string]any{
			"index":         choice.Index,
			"delta":         delta,
			"finish_reason": choice.FinishReason,
		}
		if len(choice.Logprobs) > 0 {
			renderedChoice["logprobs"] = choice.Logprobs
		}

		chunk := map[string]any{
			"id":      resp.ID,
			"object":  "chat.completion.chunk",
			"model":   resp.Model,
			"choices": []map[string]any{renderedChoice},
		}
		if resp.Created != 0 {
			chunk["created"] = resp.Created
		}
		if resp.Provider != "" {
			chunk["provider"] = resp.Provider
		}
		if resp.SystemFingerprint != "" {
			chunk["system_fingerprint"] = resp.SystemFingerprint
		}
		if err := appendSSEJSONEvent(&out, "", chunk); err != nil {
			return nil, err
		}
	}

	if usage != nil {
		chunk := map[string]any{
			"id":      resp.ID,
			"object":  "chat.completion.chunk",
			"model":   resp.Model,
			"choices": []map[string]any{},
			"usage":   usage,
		}
		if resp.Created != 0 {
			chunk["created"] = resp.Created
		}
		if resp.Provider != "" {
			chunk["provider"] = resp.Provider
		}
		if err := appendSSEJSONEvent(&out, "", chunk); err != nil {
			return nil, err
		}
	}

	out.WriteString("data: [DONE]\n\n")
	return out.Bytes(), nil
}

func renderCachedResponsesStream(requestBody, cached []byte) ([]byte, error) {
	var resp map[string]any
	if err := json.Unmarshal(cached, &resp); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	includeUsage := streamIncludeUsageRequested("/v1/responses", requestBody)
	responseWithUsage := cloneJSONMap(resp)
	if !includeUsage {
		delete(responseWithUsage, "usage")
	}

	respID, _ := responseWithUsage["id"].(string)
	respObject, _ := responseWithUsage["object"].(string)
	respModel, _ := responseWithUsage["model"].(string)
	respProvider, _ := responseWithUsage["provider"].(string)
	respCreatedAt := responseWithUsage["created_at"]
	created := map[string]any{
		"id":         respID,
		"object":     nonEmpty(respObject, "response"),
		"status":     "in_progress",
		"model":      respModel,
		"provider":   respProvider,
		"created_at": respCreatedAt,
	}
	if err := appendSSEJSONEvent(&out, "response.created", map[string]any{
		"type":     "response.created",
		"response": created,
	}); err != nil {
		return nil, err
	}

	output, _ := responseWithUsage["output"].([]any)
	for i, itemAny := range output {
		itemMap, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		itemID, _ := itemMap["id"].(string)
		added := responsesAddedItem(itemMap)
		if err := appendSSEJSONEvent(&out, "response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"item":         added,
			"output_index": i,
		}); err != nil {
			return nil, err
		}
		if err := appendResponsesItemDeltaEvents(&out, itemMap, itemID, i); err != nil {
			return nil, err
		}
		done := cloneJSONMap(itemMap)
		if _, ok := done["status"]; !ok || done["status"] == "" {
			done["status"] = "completed"
		}
		if err := appendSSEJSONEvent(&out, "response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"item":         done,
			"output_index": i,
		}); err != nil {
			return nil, err
		}
	}

	terminalEventName := responsesTerminalEventName(responseWithUsage)
	if err := appendSSEJSONEvent(&out, terminalEventName, map[string]any{
		"type":     terminalEventName,
		"response": responseWithUsage,
	}); err != nil {
		return nil, err
	}
	out.WriteString("data: [DONE]\n\n")
	return out.Bytes(), nil
}

func reconstructStreamingResponse(path string, raw []byte, defaults streamResponseDefaults) ([]byte, bool) {
	switch path {
	case "/v1/chat/completions":
		builder := &chatStreamCacheBuilder{
			defaults: defaults,
			Choices:  make(map[int]*chatChoiceState),
		}
		parseSSEJSONEvents(raw, builder.OnJSONEvent)
		return builder.Build()
	case "/v1/responses":
		builder := &responsesStreamCacheBuilder{
			defaults: defaults,
			Output:   make(map[int]*responsesOutputState),
			ItemIDs:  make(map[string]int),
		}
		parseSSEJSONEvents(raw, builder.OnJSONEvent)
		return builder.Build()
	default:
		return nil, false
	}
}

func parseSSEJSONEvents(raw []byte, onJSON func(map[string]any)) {
	for len(raw) > 0 {
		idx, sepLen := nextCacheEventBoundary(raw)
		event := raw
		if idx != -1 {
			event = raw[:idx]
			raw = raw[idx+sepLen:]
		} else {
			raw = nil
		}

		payload, ok := parseCacheEventJSON(event)
		if !ok {
			if idx == -1 {
				break
			}
			continue
		}
		onJSON(payload)
		if idx == -1 {
			break
		}
	}
}

func parseCacheEventJSON(event []byte) (map[string]any, bool) {
	lines := bytes.Split(event, []byte("\n"))
	payloadLines := make([][]byte, 0, len(lines))
	for _, line := range lines {
		data, ok := parseCacheDataLine(line)
		if !ok {
			continue
		}
		payloadLines = append(payloadLines, data)
	}
	if len(payloadLines) == 0 {
		return nil, false
	}

	jsonData := bytes.Join(payloadLines, []byte("\n"))
	if bytes.Equal(jsonData, cacheDonePayload) {
		return nil, false
	}

	var payload map[string]any
	if err := json.Unmarshal(jsonData, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func nextCacheEventBoundary(data []byte) (idx int, sepLen int) {
	lfIdx := bytes.Index(data, cacheLFEventBoundary)
	crlfIdx := bytes.Index(data, cacheCRLFEventBoundary)

	switch {
	case lfIdx == -1:
		if crlfIdx == -1 {
			return -1, 0
		}
		return crlfIdx, len(cacheCRLFEventBoundary)
	case crlfIdx == -1 || lfIdx < crlfIdx:
		return lfIdx, len(cacheLFEventBoundary)
	default:
		return crlfIdx, len(cacheCRLFEventBoundary)
	}
}

func parseCacheDataLine(line []byte) ([]byte, bool) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if !bytes.HasPrefix(line, cacheDataPrefix) {
		return nil, false
	}
	payload := bytes.TrimPrefix(line, cacheDataPrefix)
	if len(payload) > 0 && payload[0] == ' ' {
		payload = payload[1:]
	}
	return payload, true
}

func (b *chatStreamCacheBuilder) OnJSONEvent(event map[string]any) {
	if b == nil {
		return
	}
	b.seen = true

	if id, ok := event["id"].(string); ok && id != "" {
		b.ID = id
	}
	if model, ok := event["model"].(string); ok && model != "" {
		b.Model = model
	}
	if provider, ok := event["provider"].(string); ok && provider != "" {
		b.Provider = provider
	}
	if object, ok := event["object"].(string); ok && object != "" {
		b.Object = object
	}
	if fingerprint, ok := event["system_fingerprint"].(string); ok && fingerprint != "" {
		b.SystemFingerprint = fingerprint
	}
	if created, ok := jsonNumberToInt64(event["created"]); ok {
		b.Created = created
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		b.Usage = cloneJSONMap(usage)
	}

	choices, ok := event["choices"].([]any)
	if !ok {
		return
	}
	for _, choiceAny := range choices {
		choiceMap, ok := choiceAny.(map[string]any)
		if !ok {
			continue
		}
		index, ok := jsonNumberToInt(choiceMap["index"])
		if !ok {
			index = len(b.Choices)
		}
		state := b.choice(index)
		if finish, ok := choiceMap["finish_reason"].(string); ok && finish != "" {
			state.FinishReason = finish
		}
		if logprobs, ok := choiceMap["logprobs"]; ok {
			raw, err := json.Marshal(logprobs)
			if err == nil {
				state.Logprobs = raw
				state.HasLogprobs = true
			}
		}

		delta, ok := choiceMap["delta"].(map[string]any)
		if !ok {
			continue
		}
		if role, ok := delta["role"].(string); ok && role != "" {
			state.Role = role
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			_, _ = state.Content.WriteString(content)
		}
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			_, _ = state.Reasoning.WriteString(reasoning)
		}

		toolCalls, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, toolAny := range toolCalls {
			toolMap, ok := toolAny.(map[string]any)
			if !ok {
				continue
			}
			toolIndex, ok := jsonNumberToInt(toolMap["index"])
			if !ok {
				toolIndex = len(state.ToolCalls)
			}
			toolState := state.toolCall(toolIndex)
			if id, ok := toolMap["id"].(string); ok && id != "" {
				toolState.ID = id
			}
			if typ, ok := toolMap["type"].(string); ok && typ != "" {
				toolState.Type = typ
			}
			function, ok := toolMap["function"].(map[string]any)
			if !ok {
				continue
			}
			if name, ok := function["name"].(string); ok && name != "" {
				toolState.Name = name
			}
			if arguments, ok := function["arguments"].(string); ok && arguments != "" {
				_, _ = toolState.Arguments.WriteString(arguments)
			}
		}
	}
}

func (b *chatStreamCacheBuilder) Build() ([]byte, bool) {
	if b == nil || !b.seen {
		return nil, false
	}

	choiceIndexes := make([]int, 0, len(b.Choices))
	for index := range b.Choices {
		choiceIndexes = append(choiceIndexes, index)
	}
	sort.Ints(choiceIndexes)

	choices := make([]map[string]any, 0, len(choiceIndexes))
	for _, index := range choiceIndexes {
		state := b.Choices[index]
		message := map[string]any{
			"role": nonEmpty(state.Role, "assistant"),
		}

		content := state.Content.String()
		toolCalls := buildChatToolCalls(state.ToolCalls)
		switch {
		case content != "":
			message["content"] = content
		case len(toolCalls) > 0:
			message["content"] = nil
		default:
			message["content"] = ""
		}
		if len(toolCalls) > 0 {
			message["tool_calls"] = toolCalls
		}
		if reasoning := state.Reasoning.String(); reasoning != "" {
			message["reasoning_content"] = reasoning
		}

		choice := map[string]any{
			"index":         index,
			"message":       message,
			"finish_reason": state.FinishReason,
		}
		if state.HasLogprobs {
			choice["logprobs"] = state.Logprobs
		}
		choices = append(choices, choice)
	}

	response := map[string]any{
		"id":      b.ID,
		"object":  "chat.completion",
		"model":   nonEmpty(b.Model, b.defaults.Model),
		"choices": choices,
	}
	if provider := nonEmpty(b.Provider, b.defaults.Provider); provider != "" {
		response["provider"] = provider
	}
	if b.Created != 0 {
		response["created"] = b.Created
	}
	if b.SystemFingerprint != "" {
		response["system_fingerprint"] = b.SystemFingerprint
	}
	if b.Usage != nil {
		response["usage"] = b.Usage
	}

	data, err := json.Marshal(response)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (b *chatStreamCacheBuilder) choice(index int) *chatChoiceState {
	state, ok := b.Choices[index]
	if ok {
		return state
	}
	state = &chatChoiceState{
		Index:     index,
		ToolCalls: make(map[int]*chatToolCallState),
	}
	b.Choices[index] = state
	return state
}

func (c *chatChoiceState) toolCall(index int) *chatToolCallState {
	state, ok := c.ToolCalls[index]
	if ok {
		return state
	}
	state = &chatToolCallState{Index: index}
	c.ToolCalls[index] = state
	return state
}

func (b *responsesStreamCacheBuilder) OnJSONEvent(event map[string]any) {
	if b == nil {
		return
	}
	b.seen = true

	eventType, _ := event["type"].(string)
	switch eventType {
	case "response.created", "response.completed", "response.failed", "response.incomplete", "response.done":
		response, ok := event["response"].(map[string]any)
		if !ok {
			return
		}
		b.captureResponseMetadata(response)
		if output, ok := response["output"].([]any); ok {
			for index, itemAny := range output {
				itemMap, ok := itemAny.(map[string]any)
				if !ok {
					continue
				}
				b.output(index).SetItem(itemMap)
				if itemID, _ := itemMap["id"].(string); itemID != "" {
					b.ItemIDs[itemID] = index
				}
				if itemType, _ := itemMap["type"].(string); itemType == "message" {
					if role, _ := itemMap["role"].(string); role == "assistant" {
						b.AssistantIndex = index
						b.HasAssistant = true
					}
				} else if itemType == "reasoning" {
					b.ReasoningIndex = index
					b.HasReasoning = true
				}
			}
		}
	case "response.output_item.added", "response.output_item.done":
		index, ok := jsonNumberToInt(event["output_index"])
		if !ok {
			return
		}
		item, ok := event["item"].(map[string]any)
		if !ok {
			return
		}
		state := b.output(index)
		state.SetItem(item)
		if itemID, _ := item["id"].(string); itemID != "" {
			b.ItemIDs[itemID] = index
		}
		if itemType, _ := item["type"].(string); itemType == "message" {
			if role, _ := item["role"].(string); role == "assistant" {
				b.AssistantIndex = index
				b.HasAssistant = true
			}
		} else if itemType == "reasoning" {
			b.ReasoningIndex = index
			b.HasReasoning = true
		}
	case "response.output_text.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return
		}
		contentIndex, _ := jsonNumberToInt(event["content_index"])
		index, ok := b.lookupOutputIndex(event)
		if !ok {
			index = 0
			if b.HasAssistant {
				index = b.AssistantIndex
			}
		}
		b.rememberOutputLocator(event, index)
		b.AssistantIndex = index
		b.HasAssistant = true
		b.output(index).AppendText(contentIndex, delta)
	case "response.reasoning_text.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return
		}
		contentIndex, _ := jsonNumberToInt(event["content_index"])
		outputIndex, hasOutputIndex := jsonNumberToInt(event["output_index"])
		index, ok := b.lookupOutputIndex(event)
		if !ok {
			index = b.ensureReasoningOutputIndex(outputIndex, hasOutputIndex)
		}
		b.rememberOutputLocator(event, index)
		b.ReasoningIndex = index
		b.HasReasoning = true
		b.output(index).AppendReasoning(contentIndex, delta)
	case "response.function_call_arguments.delta":
		index, ok := b.lookupOutputIndex(event)
		if !ok {
			return
		}
		delta, _ := event["delta"].(string)
		if delta == "" {
			return
		}
		b.output(index).AppendArguments(delta)
	case "response.function_call_arguments.done":
		index, ok := b.lookupOutputIndex(event)
		if !ok {
			return
		}
		arguments, _ := event["arguments"].(string)
		b.output(index).SetArguments(arguments)
	}
}

func (b *responsesStreamCacheBuilder) Build() ([]byte, bool) {
	if b == nil || !b.seen {
		return nil, false
	}

	indexes := make([]int, 0, len(b.Output))
	for index := range b.Output {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	output := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		item := b.Output[index].BuildItem()
		if len(item) == 0 {
			continue
		}
		output = append(output, item)
	}

	response := cloneJSONMap(b.Response)
	if response == nil {
		response = map[string]any{
			"id":         b.ID,
			"object":     nonEmpty(b.Object, "response"),
			"created_at": b.CreatedAt,
			"model":      nonEmpty(b.Model, b.defaults.Model),
			"status":     nonEmpty(b.Status, "completed"),
		}
		if provider := nonEmpty(b.Provider, b.defaults.Provider); provider != "" {
			response["provider"] = provider
		}
		if b.Usage != nil {
			response["usage"] = b.Usage
		}
		if b.Error != nil {
			response["error"] = b.Error
		}
	}
	response["output"] = output
	if _, ok := response["id"]; !ok {
		response["id"] = b.ID
	}
	if _, ok := response["object"]; !ok {
		response["object"] = nonEmpty(b.Object, "response")
	}
	if _, ok := response["created_at"]; !ok && b.CreatedAt != 0 {
		response["created_at"] = b.CreatedAt
	}
	if _, ok := response["model"]; !ok {
		response["model"] = nonEmpty(b.Model, b.defaults.Model)
	}
	if _, ok := response["status"]; !ok {
		response["status"] = nonEmpty(b.Status, "completed")
	}
	if provider := nonEmpty(b.Provider, b.defaults.Provider); provider != "" {
		if _, ok := response["provider"]; !ok {
			response["provider"] = provider
		}
	}
	if b.Usage != nil {
		if _, ok := response["usage"]; !ok {
			response["usage"] = b.Usage
		}
	}
	if b.Error != nil {
		if _, ok := response["error"]; !ok {
			response["error"] = b.Error
		}
	}

	data, err := json.Marshal(response)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (b *responsesStreamCacheBuilder) captureResponseMetadata(response map[string]any) {
	b.Response = cloneJSONMap(response)
	if id, ok := response["id"].(string); ok && id != "" {
		b.ID = id
	}
	if object, ok := response["object"].(string); ok && object != "" {
		b.Object = object
	}
	if model, ok := response["model"].(string); ok && model != "" {
		b.Model = model
	}
	if provider, ok := response["provider"].(string); ok && provider != "" {
		b.Provider = provider
	}
	if status, ok := response["status"].(string); ok && status != "" {
		b.Status = status
	}
	if createdAt, ok := jsonNumberToInt64(response["created_at"]); ok {
		b.CreatedAt = createdAt
	}
	if usage, ok := response["usage"].(map[string]any); ok {
		b.Usage = cloneJSONMap(usage)
	}
	if errMap, ok := response["error"].(map[string]any); ok {
		b.Error = cloneJSONMap(errMap)
	}
}

func (b *responsesStreamCacheBuilder) output(index int) *responsesOutputState {
	state, ok := b.Output[index]
	if ok {
		return state
	}
	state = &responsesOutputState{Index: index}
	b.Output[index] = state
	return state
}

func (b *responsesStreamCacheBuilder) lookupOutputIndex(event map[string]any) (int, bool) {
	if index, ok := jsonNumberToInt(event["output_index"]); ok {
		return index, true
	}
	itemID, _ := event["item_id"].(string)
	index, ok := b.ItemIDs[itemID]
	return index, ok
}

func (b *responsesStreamCacheBuilder) rememberOutputLocator(event map[string]any, index int) {
	itemID, _ := event["item_id"].(string)
	if itemID == "" {
		return
	}
	b.ItemIDs[itemID] = index
}

func (b *responsesStreamCacheBuilder) ensureReasoningOutputIndex(outputIndex int, hasOutputIndex bool) int {
	if hasOutputIndex {
		b.ReasoningIndex = outputIndex
		b.HasReasoning = true
		return outputIndex
	}
	if b.HasReasoning {
		return b.ReasoningIndex
	}
	index := len(b.Output)
	b.ReasoningIndex = index
	b.HasReasoning = true
	return index
}

func (s *responsesOutputState) SetItem(item map[string]any) {
	s.Item = cloneJSONMap(item)
}

func (s *responsesOutputState) AppendText(contentIndex int, delta string) {
	if delta == "" {
		return
	}
	part := s.textPart(contentIndex)
	_, _ = part.WriteString(delta)
}

func (s *responsesOutputState) AppendReasoning(contentIndex int, delta string) {
	if delta == "" {
		return
	}
	part := s.reasoningPart(contentIndex)
	_, _ = part.WriteString(delta)
}

func (s *responsesOutputState) AppendArguments(delta string) {
	if delta == "" {
		return
	}
	_, _ = s.Arguments.WriteString(delta)
	s.HasArgs = true
}

func (s *responsesOutputState) SetArguments(arguments string) {
	s.Arguments = strings.Builder{}
	_, _ = s.Arguments.WriteString(arguments)
	s.HasArgs = true
}

func (s *responsesOutputState) textPart(contentIndex int) *strings.Builder {
	if s.TextParts == nil {
		s.TextParts = make(map[int]*strings.Builder)
	}
	part, ok := s.TextParts[contentIndex]
	if ok {
		return part
	}
	part = &strings.Builder{}
	s.TextParts[contentIndex] = part
	return part
}

func (s *responsesOutputState) reasoningPart(contentIndex int) *strings.Builder {
	if s.ReasoningParts == nil {
		s.ReasoningParts = make(map[int]*strings.Builder)
	}
	part, ok := s.ReasoningParts[contentIndex]
	if ok {
		return part
	}
	part = &strings.Builder{}
	s.ReasoningParts[contentIndex] = part
	return part
}

func (s *responsesOutputState) BuildItem() map[string]any {
	item := cloneJSONMap(s.Item)
	if item == nil {
		item = make(map[string]any)
	}

	itemType, _ := item["type"].(string)
	if len(s.TextParts) > 0 {
		if itemType == "" {
			itemType = "message"
			item["type"] = itemType
		}
		if item["role"] == nil {
			item["role"] = "assistant"
		}
		item["content"] = buildResponsesContentParts(item["content"], "output_text", s.TextParts)
	}
	if len(s.ReasoningParts) > 0 {
		if itemType == "" {
			itemType = "reasoning"
			item["type"] = itemType
		}
		targetField := "content"
		if itemType == "reasoning" {
			targetField = "summary"
		} else {
			if _, ok := item["summary"].([]any); ok {
				targetField = "summary"
			}
		}
		item[targetField] = buildResponsesContentParts(item[targetField], "reasoning_text", s.ReasoningParts)
	}
	if s.HasArgs {
		if itemType == "" {
			itemType = "function_call"
			item["type"] = itemType
		}
		item["arguments"] = s.Arguments.String()
	}
	if _, ok := item["status"]; !ok || item["status"] == "" {
		item["status"] = "completed"
	}

	return item
}

func buildResponsesContentParts(existing any, partType string, parts map[int]*strings.Builder) []map[string]any {
	if len(parts) == 0 {
		return nil
	}

	existingParts, _ := existing.([]any)
	maxIndex := len(existingParts) - 1
	for index := range parts {
		if index > maxIndex {
			maxIndex = index
		}
	}

	built := make([]map[string]any, 0, maxIndex+1)
	for index := 0; index <= maxIndex; index++ {
		existingPart, existingOK := cloneJSONPart(existingParts, index)
		partBuilder, hasPart := parts[index]

		switch {
		case hasPart:
			if existingPart == nil {
				existingPart = make(map[string]any)
			}
			existingPart["type"] = partType
			existingPart["text"] = partBuilder.String()
			built = append(built, existingPart)
		case existingOK:
			built = append(built, existingPart)
		}
	}

	return built
}

func cloneJSONPart(parts []any, index int) (map[string]any, bool) {
	if index < 0 || index >= len(parts) {
		return nil, false
	}
	part, ok := parts[index].(map[string]any)
	if !ok {
		return nil, false
	}
	return cloneJSONMap(part), true
}

func buildChatToolCalls(states map[int]*chatToolCallState) []map[string]any {
	if len(states) == 0 {
		return nil
	}

	indexes := make([]int, 0, len(states))
	for index := range states {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	toolCalls := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		state := states[index]
		toolCall := map[string]any{
			"id":    state.ID,
			"type":  nonEmpty(state.Type, "function"),
			"index": index,
			"function": map[string]any{
				"name":      state.Name,
				"arguments": state.Arguments.String(),
			},
		}
		toolCalls = append(toolCalls, toolCall)
	}
	return toolCalls
}

func renderChatToolCalls(toolCalls []core.ToolCall) []map[string]any {
	if len(toolCalls) == 0 {
		return nil
	}
	rendered := make([]map[string]any, 0, len(toolCalls))
	for index, toolCall := range toolCalls {
		rendered = append(rendered, map[string]any{
			"index": index,
			"id":    toolCall.ID,
			"type":  nonEmpty(toolCall.Type, "function"),
			"function": map[string]any{
				"name":      toolCall.Function.Name,
				"arguments": toolCall.Function.Arguments,
			},
		})
	}
	return rendered
}

func normalizeStreamOptionsForCache(src *core.StreamOptions) *core.StreamOptions {
	if src == nil || !src.IncludeUsage {
		return nil
	}
	cloned := *src
	return &cloned
}

func streamIncludeUsageRequested(path string, requestBody []byte) bool {
	switch path {
	case "/v1/chat/completions":
		req, err := core.DecodeChatRequest(requestBody, nil)
		if err != nil || req == nil {
			return false
		}
		return normalizeStreamOptionsForCache(req.StreamOptions) != nil
	case "/v1/responses":
		req, err := core.DecodeResponsesRequest(requestBody, nil)
		if err != nil || req == nil {
			return false
		}
		return normalizeStreamOptionsForCache(req.StreamOptions) != nil
	default:
		return false
	}
}

func chatReasoningContent(message core.ResponseMessage) string {
	raw := message.ExtraFields.Lookup("reasoning_content")
	if len(raw) == 0 {
		return ""
	}
	var reasoning string
	if err := json.Unmarshal(raw, &reasoning); err != nil {
		return ""
	}
	return reasoning
}

func responsesAddedItem(item map[string]any) map[string]any {
	added := cloneJSONMap(item)
	if added == nil {
		return nil
	}
	added["status"] = "in_progress"
	delete(added, "arguments")
	if _, ok := added["content"].([]any); ok {
		added["content"] = []any{}
	}
	if _, ok := added["summary"].([]any); ok {
		added["summary"] = []any{}
	}
	return added
}

func responsesTerminalEventName(response map[string]any) string {
	status, _ := response["status"].(string)
	switch status {
	case "failed":
		return "response.failed"
	case "incomplete":
		return "response.incomplete"
	default:
		return "response.completed"
	}
}

func appendResponsesItemDeltaEvents(out *bytes.Buffer, item map[string]any, itemID string, outputIndex int) error {
	if out == nil || item == nil {
		return nil
	}

	if arguments, ok := item["arguments"].(string); ok && arguments != "" {
		if err := appendSSEJSONEvent(out, "response.function_call_arguments.delta", map[string]any{
			"type":         "response.function_call_arguments.delta",
			"item_id":      itemID,
			"output_index": outputIndex,
			"delta":        arguments,
		}); err != nil {
			return err
		}
		if err := appendSSEJSONEvent(out, "response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      itemID,
			"output_index": outputIndex,
			"arguments":    arguments,
		}); err != nil {
			return err
		}
	}

	for _, key := range []string{"content", "summary"} {
		parts, ok := item[key].([]any)
		if !ok {
			continue
		}
		for contentIndex, partAny := range parts {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}
			eventName, payload, ok := responsesContentDeltaEvent(part, itemID, outputIndex, contentIndex)
			if !ok {
				continue
			}
			if err := appendSSEJSONEvent(out, eventName, payload); err != nil {
				return err
			}
		}
	}

	return nil
}

func responsesContentDeltaEvent(part map[string]any, itemID string, outputIndex, contentIndex int) (string, map[string]any, bool) {
	partType, _ := part["type"].(string)
	text, _ := part["text"].(string)
	if partType == "" || text == "" {
		return "", nil, false
	}

	var eventName string
	switch partType {
	case "output_text":
		eventName = "response.output_text.delta"
	case "reasoning_text":
		eventName = "response.reasoning_text.delta"
	default:
		return "", nil, false
	}

	payload := map[string]any{
		"type":          eventName,
		"delta":         text,
		"output_index":  outputIndex,
		"content_index": contentIndex,
	}
	if itemID != "" {
		payload["item_id"] = itemID
	}

	return eventName, payload, true
}

func chatUsageMap(usage core.Usage) map[string]any {
	if usage.PromptTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.PromptTokensDetails == nil &&
		usage.CompletionTokensDetails == nil &&
		len(usage.RawUsage) == 0 {
		return nil
	}
	result, err := toJSONMap(usage)
	if err != nil {
		return nil
	}
	return result
}

func appendSSEJSONEvent(out *bytes.Buffer, eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if eventName != "" {
		out.WriteString("event: ")
		out.WriteString(eventName)
		out.WriteByte('\n')
	}
	out.WriteString("data: ")
	out.Write(data)
	out.WriteString("\n\n")
	return nil
}

func toJSONMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func cloneJSONMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func jsonNumberToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func jsonNumberToInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
