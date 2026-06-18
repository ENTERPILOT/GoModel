package responsecache

import (
	"bytes"
	"sort"
	"strings"

	"github.com/goccy/go-json"
)

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
