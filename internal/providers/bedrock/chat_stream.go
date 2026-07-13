package bedrock

import (
	"context"
	"io"
	"strconv"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// StreamChatCompletion runs Bedrock ConverseStream and exposes an
// OpenAI-compatible SSE stream.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("bedrock chat request is required", nil)
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		return nil, err
	}

	out, err := p.runtime.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId:         parts.modelID,
		Messages:        parts.messages,
		System:          parts.system,
		InferenceConfig: parts.infCfg,
		ToolConfig:      parts.toolCfg,
	})
	if err != nil {
		return nil, mapAWSError(err)
	}

	return newOpenAIStream(out, req.Model), nil
}

// streamConverter consumes a Bedrock ConverseStream event channel and emits
// OpenAI-compatible SSE chunks via io.Reader. Reads are buffered: each loop
// iteration consumes events until at least one chunk's worth of data is ready
// to be returned.
//
// Bedrock emits messageStop before its trailing metadata event, but the OpenAI
// streaming convention puts usage on the final chunk that carries
// finish_reason. We therefore defer emitting the finish chunk until either
// the metadata arrives (so we can include usage) or the event stream closes
// (so we emit a finish chunk without usage rather than swallow it).
type streamConverter struct {
	stream    *bedrockruntime.ConverseStreamOutput
	model     string
	id        string
	created   int64
	closeOnce sync.Once
	buf       []byte
	done      bool

	// per-block tool-use accumulators keyed by content_block index.
	toolByIndex map[int32]*toolStreamState

	usage *brtypes.TokenUsage

	// Deferred-finish state. pendingStopReason is captured at messageStop and
	// drained at metadata or channel-close, whichever comes first.
	pendingStopReason brtypes.StopReason
	havePendingStop   bool
	finishSent        bool
}

type toolStreamState struct {
	openAIIndex int
	id          string
	name        string
}

func newOpenAIStream(out *bedrockruntime.ConverseStreamOutput, model string) *streamConverter {
	now := time.Now()
	return &streamConverter{
		stream:      out,
		model:       model,
		id:          "bedrock-" + strconv.FormatInt(now.UnixNano(), 10),
		created:     now.Unix(),
		toolByIndex: make(map[int32]*toolStreamState),
	}
}

func (s *streamConverter) Read(p []byte) (int, error) {
	if len(s.buf) > 0 {
		n := copy(p, s.buf)
		s.buf = s.buf[n:]
		return n, nil
	}
	if s.done {
		return 0, io.EOF
	}

	events := s.stream.GetStream().Events()
	for {
		ev, ok := <-events
		if !ok {
			if streamErr := s.stream.GetStream().Err(); streamErr != nil {
				s.done = true
				return 0, mapAWSError(streamErr)
			}
			// Metadata never arrived — emit the deferred finish chunk now
			// (without usage) so we never swallow it.
			s.flushFinish()
			s.append("data: [DONE]\n\n")
			s.done = true
			break
		}
		s.handleEvent(ev)
		if len(s.buf) > 0 {
			break
		}
	}
	if len(s.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *streamConverter) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.done = true
		err = s.stream.GetStream().Close()
	})
	return err
}

func (s *streamConverter) append(chunk string) {
	if chunk == "" {
		return
	}
	s.buf = append(s.buf, []byte(chunk)...)
}

func (s *streamConverter) handleEvent(ev brtypes.ConverseStreamOutput) {
	switch e := ev.(type) {
	case *brtypes.ConverseStreamOutputMemberMessageStart:
		role := string(e.Value.Role)
		if role == "" {
			role = "assistant"
		}
		s.append(s.formatChunk(map[string]any{"role": role}, nil, nil))

	case *brtypes.ConverseStreamOutputMemberContentBlockStart:
		if e.Value.Start == nil {
			return
		}
		if start, ok := e.Value.Start.(*brtypes.ContentBlockStartMemberToolUse); ok {
			idx := awssdk.ToInt32(e.Value.ContentBlockIndex)
			state := &toolStreamState{
				openAIIndex: len(s.toolByIndex),
				id:          awssdk.ToString(start.Value.ToolUseId),
				name:        awssdk.ToString(start.Value.Name),
			}
			s.toolByIndex[idx] = state
			s.append(s.formatChunk(map[string]any{
				"tool_calls": []map[string]any{{
					"index": state.openAIIndex,
					"id":    state.id,
					"type":  "function",
					"function": map[string]any{
						"name":      state.name,
						"arguments": "",
					},
				}},
			}, nil, nil))
		}

	case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
		if e.Value.Delta == nil {
			return
		}
		switch d := e.Value.Delta.(type) {
		case *brtypes.ContentBlockDeltaMemberText:
			if d.Value != "" {
				s.append(s.formatChunk(map[string]any{"content": d.Value}, nil, nil))
			}
		case *brtypes.ContentBlockDeltaMemberToolUse:
			idx := awssdk.ToInt32(e.Value.ContentBlockIndex)
			state := s.toolByIndex[idx]
			if state == nil {
				return
			}
			args := awssdk.ToString(d.Value.Input)
			if args == "" {
				return
			}
			s.append(s.formatChunk(map[string]any{
				"tool_calls": []map[string]any{{
					"index": state.openAIIndex,
					"function": map[string]any{
						"arguments": args,
					},
				}},
			}, nil, nil))
		}

	case *brtypes.ConverseStreamOutputMemberContentBlockStop:
		// nothing to emit; the closing chunk is sent at message_stop.

	case *brtypes.ConverseStreamOutputMemberMessageStop:
		// Defer the terminal chunk; we want to include usage from metadata.
		s.pendingStopReason = e.Value.StopReason
		s.havePendingStop = true

	case *brtypes.ConverseStreamOutputMemberMetadata:
		if e.Value.Usage != nil {
			s.usage = e.Value.Usage
		}
		s.flushFinish()
	}
}

// flushFinish emits the deferred finish chunk if one is pending and has not
// already been sent. Safe to call multiple times.
func (s *streamConverter) flushFinish() {
	if !s.havePendingStop || s.finishSent {
		return
	}
	hasTools := len(s.toolByIndex) > 0
	finishReason := mapStopReason(s.pendingStopReason, hasTools)
	s.append(s.formatChunk(map[string]any{}, finishReason, s.usage))
	s.finishSent = true
}

func (s *streamConverter) formatChunk(delta map[string]any, finishReason any, usage *brtypes.TokenUsage) string {
	var usagePayload map[string]any
	if usage != nil {
		usagePayload = map[string]any{
			"prompt_tokens":     int(awssdk.ToInt32(usage.InputTokens)),
			"completion_tokens": int(awssdk.ToInt32(usage.OutputTokens)),
			"total_tokens":      int(awssdk.ToInt32(usage.TotalTokens)),
		}
	}
	return providers.FormatChatChunkSSE(s.id, s.created, s.model, providerName, delta, finishReason, usagePayload)
}

var _ io.ReadCloser = (*streamConverter)(nil)
