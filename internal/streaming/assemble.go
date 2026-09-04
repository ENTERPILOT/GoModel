package streaming

import (
	"errors"
	"sort"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ErrNoEvents is returned when a response cannot be assembled because the
// stream carried no decodable payload.
var ErrNoEvents = errors.New("streaming: no events to assemble")

type chatAssembleChunk struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	Provider          string `json:"provider"`
	SystemFingerprint string `json:"system_fingerprint"`
	Created           int64  `json:"created"`
	Choices           []struct {
		Index int `json:"index"`
		Delta *struct {
			Role             string          `json:"role"`
			Content          *string         `json:"content"`
			ReasoningContent json.RawMessage `json:"reasoning_content"`
			Reasoning        json.RawMessage `json:"reasoning"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *core.Usage `json:"usage"`
}

type chatAssembledChoice struct {
	index        int
	role         string
	content      []byte
	hasContent   bool
	reasoning    []byte
	reasoningKey string
	toolCalls    map[int]*core.ToolCall
	toolOrder    []int
	finishReason string
}

// AssembleChatResponse rebuilds a chat completion from the chunks of a chat
// stream: text, reasoning and tool call deltas are concatenated per choice,
// finish_reason and usage are taken from the chunks carrying them, and the
// envelope (id, model, created, system_fingerprint, provider) from the first
// chunk that has each member.
func AssembleChatResponse(events []Event) (*core.ChatResponse, error) {
	resp := &core.ChatResponse{Object: "chat.completion"}
	choices := make(map[int]*chatAssembledChoice)
	var order []int
	decoded := 0
	for i := range events {
		ev := &events[i]
		if len(ev.Data) == 0 || ev.Data[0] != '{' {
			continue
		}
		var chunk chatAssembleChunk
		if err := json.Unmarshal(ev.Data, &chunk); err != nil {
			continue
		}
		decoded++
		fillChatEnvelope(resp, &chunk)
		if chunk.Usage != nil {
			resp.Usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			state := choices[choice.Index]
			if state == nil {
				state = &chatAssembledChoice{index: choice.Index, toolCalls: make(map[int]*core.ToolCall)}
				choices[choice.Index] = state
				order = append(order, choice.Index)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				state.finishReason = *choice.FinishReason
			}
			delta := choice.Delta
			if delta == nil {
				continue
			}
			if delta.Role != "" {
				state.role = delta.Role
			}
			if delta.Content != nil {
				state.hasContent = true
				state.content = append(state.content, *delta.Content...)
			}
			if text, ok := jsonStringOf(delta.ReasoningContent); ok {
				state.reasoning, state.reasoningKey = append(state.reasoning, text...), "reasoning_content"
			} else if text, ok := jsonStringOf(delta.Reasoning); ok {
				state.reasoning = append(state.reasoning, text...)
				if state.reasoningKey == "" {
					state.reasoningKey = "reasoning"
				}
			}
			for pos, call := range delta.ToolCalls {
				index := pos
				if call.Index != nil {
					index = *call.Index
				}
				tool := state.toolCalls[index]
				if tool == nil {
					tool = &core.ToolCall{}
					state.toolCalls[index] = tool
					state.toolOrder = append(state.toolOrder, index)
				}
				if call.ID != "" {
					tool.ID = call.ID
				}
				if call.Type != "" {
					tool.Type = call.Type
				}
				if call.Function != nil {
					if call.Function.Name != "" {
						tool.Function.Name = call.Function.Name
					}
					tool.Function.Arguments += call.Function.Arguments
				}
			}
		}
	}
	if decoded == 0 {
		return nil, ErrNoEvents
	}
	sort.Ints(order)
	resp.Choices = make([]core.Choice, 0, len(order))
	for _, index := range order {
		choice, err := choices[index].build()
		if err != nil {
			return nil, err
		}
		resp.Choices = append(resp.Choices, choice)
	}
	return resp, nil
}

func fillChatEnvelope(resp *core.ChatResponse, chunk *chatAssembleChunk) {
	if resp.ID == "" {
		resp.ID = chunk.ID
	}
	if resp.Model == "" {
		resp.Model = chunk.Model
	}
	if resp.Provider == "" {
		resp.Provider = chunk.Provider
	}
	if resp.SystemFingerprint == "" {
		resp.SystemFingerprint = chunk.SystemFingerprint
	}
	if resp.Created == 0 {
		resp.Created = chunk.Created
	}
}

func (c *chatAssembledChoice) build() (core.Choice, error) {
	role := c.role
	if role == "" {
		role = "assistant"
	}
	message := core.ResponseMessage{Role: role}
	if c.hasContent || len(c.toolOrder) == 0 {
		message.Content = string(c.content)
	}
	sort.Ints(c.toolOrder)
	for _, index := range c.toolOrder {
		tool := *c.toolCalls[index]
		if tool.Type == "" {
			tool.Type = "function"
		}
		message.ToolCalls = append(message.ToolCalls, tool)
	}
	if c.reasoningKey != "" {
		raw, err := json.Marshal(string(c.reasoning))
		if err != nil {
			return core.Choice{}, err
		}
		message.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{c.reasoningKey: raw})
	}
	return core.Choice{Index: c.index, Message: message, FinishReason: c.finishReason}, nil
}

type responsesAssembleEvent struct {
	Type         string                    `json:"type"`
	Response     *core.ResponsesResponse   `json:"response"`
	Item         *core.ResponsesOutputItem `json:"item"`
	OutputIndex  int                       `json:"output_index"`
	ContentIndex int                       `json:"content_index"`
	Delta        string                    `json:"delta"`
}

// AssembleResponsesResponse rebuilds a Responses API response from its
// stream. The response object carried by response.completed, incomplete or
// failed wins; without one, output items are rebuilt from output_item and
// delta events (text deltas without an item become one message item) and
// the status is "incomplete".
func AssembleResponsesResponse(events []Event) (*core.ResponsesResponse, error) {
	var base *core.ResponsesResponse
	items := make(map[int]*core.ResponsesOutputItem)
	var order []int
	var looseText []byte
	decoded := 0
	item := func(index int) *core.ResponsesOutputItem {
		if existing := items[index]; existing != nil {
			return existing
		}
		created := &core.ResponsesOutputItem{Type: "message", Role: "assistant"}
		items[index] = created
		order = append(order, index)
		return created
	}
	for i := range events {
		ev := &events[i]
		if len(ev.Data) == 0 || ev.Data[0] != '{' {
			continue
		}
		var event responsesAssembleEvent
		if err := json.Unmarshal(ev.Data, &event); err != nil {
			continue
		}
		decoded++
		switch event.Type {
		case "response.completed", "response.incomplete", "response.failed", "response.done":
			if event.Response != nil {
				resp := *event.Response
				if resp.Object == "" {
					resp.Object = "response"
				}
				if resp.Status == "" {
					resp.Status = "completed"
				}
				return &resp, nil
			}
		case "response.created", "response.in_progress":
			if event.Response != nil && base == nil {
				base = event.Response
			}
		case "response.output_item.added", "response.output_item.done":
			if event.Item != nil {
				if _, seen := items[event.OutputIndex]; !seen {
					order = append(order, event.OutputIndex)
				}
				items[event.OutputIndex] = event.Item
			}
		case "response.output_text.delta":
			if len(items) == 0 && len(order) == 0 {
				looseText = append(looseText, event.Delta...)
				continue
			}
			appendOutputText(item(event.OutputIndex), event.ContentIndex, event.Delta)
		case "response.function_call_arguments.delta":
			item(event.OutputIndex).Arguments += event.Delta
		}
	}
	if decoded == 0 {
		return nil, ErrNoEvents
	}
	resp := &core.ResponsesResponse{Object: "response", Status: "incomplete"}
	if base != nil {
		resp.ID, resp.Model, resp.Provider, resp.CreatedAt = base.ID, base.Model, base.Provider, base.CreatedAt
	}
	sort.Ints(order)
	for _, index := range order {
		resp.Output = append(resp.Output, *items[index])
	}
	if len(looseText) > 0 {
		resp.Output = append(resp.Output, core.ResponsesOutputItem{
			Type:    "message",
			Role:    "assistant",
			Status:  "incomplete",
			Content: []core.ResponsesContentItem{{Type: "output_text", Text: string(looseText)}},
		})
	}
	return resp, nil
}

func appendOutputText(item *core.ResponsesOutputItem, contentIndex int, delta string) {
	for len(item.Content) <= contentIndex {
		item.Content = append(item.Content, core.ResponsesContentItem{Type: "output_text"})
	}
	item.Content[contentIndex].Text += delta
}
