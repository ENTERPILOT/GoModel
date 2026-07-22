package server

import (
	"net/http"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

func conversationItemList(items []json.RawMessage, hasMore bool, include []string) core.ConversationItemListResponse {
	data := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		data = append(data, conversationItemForInclude(item, include))
	}
	response := core.ConversationItemListResponse{Object: "list", Data: data, HasMore: hasMore}
	if len(data) > 0 {
		firstID := responseInputItemID(data[0])
		lastID := responseInputItemID(data[len(data)-1])
		response.FirstID = &firstID
		response.LastID = &lastID
	}
	return response
}

func paginateConversationItems(items []json.RawMessage, params core.ConversationItemListParams) (core.ConversationItemListResponse, *core.GatewayError) {
	count := len(items)
	start := 0
	if params.After != "" {
		position := -1
		for pos := range count {
			if responseInputItemID(items[orderedInputItemIndex(count, pos, params.Order)]) == params.After {
				position = pos
				break
			}
		}
		if position < 0 {
			return core.ConversationItemListResponse{}, core.NewInvalidRequestErrorWithStatus(
				http.StatusNotFound, "No item found with id '"+params.After+"'", nil,
			).WithParam("after")
		}
		start = position + 1
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	remaining := max(count-start, 0)
	hasMore := remaining > limit
	remaining = min(remaining, limit)
	data := make([]json.RawMessage, 0, remaining)
	for offset := 0; offset < remaining; offset++ {
		index := orderedInputItemIndex(count, start+offset, params.Order)
		data = append(data, core.CloneRawJSON(items[index]))
	}
	return conversationItemList(data, hasMore, params.Include), nil
}

func conversationItemIncludes(c *echo.Context) []string {
	query := c.Request().URL.Query()
	return appendQueryArray(query["include"], query["include[]"])
}

func conversationItemForInclude(raw json.RawMessage, include []string) json.RawMessage {
	requested := make(map[string]struct{}, len(include))
	for _, value := range include {
		requested[value] = struct{}{}
	}
	has := func(value string) bool {
		_, ok := requested[value]
		return ok
	}

	item, err := decodeRawJSONObject(raw)
	if err != nil {
		return core.CloneRawJSON(raw)
	}
	switch rawJSONString(item, "type") {
	case "reasoning":
		if !has("reasoning.encrypted_content") {
			delete(item, "encrypted_content")
		}
	case "message":
		var content []json.RawMessage
		if err := json.Unmarshal(item["content"], &content); err == nil {
			for index, rawPart := range content {
				part, err := decodeRawJSONObject(rawPart)
				if err != nil {
					continue
				}
				switch rawJSONString(part, "type") {
				case "input_image":
					if !has("message.input_image.image_url") {
						delete(part, "image_url")
					}
				case "output_text":
					if !has("message.output_text.logprobs") {
						delete(part, "logprobs")
					}
				}
				content[index], _ = json.Marshal(part)
			}
			item["content"], _ = json.Marshal(content)
		}
	case "file_search_call":
		if !has("file_search_call.results") {
			delete(item, "results")
		}
	case "web_search_call":
		if !has("web_search_call.results") {
			delete(item, "results")
		}
		if !has("web_search_call.action.sources") {
			if action, err := decodeRawJSONObject(item["action"]); err == nil {
				delete(action, "sources")
				item["action"], _ = json.Marshal(action)
			}
		}
	case "code_interpreter_call":
		if !has("code_interpreter_call.outputs") {
			delete(item, "outputs")
		}
	case "computer_call_output":
		if !has("computer_call_output.output.image_url") {
			if output, err := decodeRawJSONObject(item["output"]); err == nil {
				delete(output, "image_url")
				item["output"], _ = json.Marshal(output)
			}
		}
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return core.CloneRawJSON(raw)
	}
	return encoded
}
