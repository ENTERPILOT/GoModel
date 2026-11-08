package session

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// benchChatBody builds a chat request whose user message pads the body to at
// least target bytes, optionally carrying tools function definitions the way
// coding agents send them on every turn.
func benchChatBody(target, tools int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"model":"gpt-4o-mini","stream":true,"temperature":0.2,`)
	if tools > 0 {
		buf.WriteString(`"tools":[`)
		for i := range tools {
			if i > 0 {
				buf.WriteByte(',')
			}
			fmt.Fprintf(&buf, `{"type":"function","function":{"name":"tool_%d","description":"Reads, edits or searches files in the workspace and reports the result back to the model so it can continue.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Absolute path of the file to operate on."},"offset":{"type":"integer","description":"Line to start from."},"limit":{"type":"integer","description":"Number of lines to read."},"pattern":{"type":"string","description":"Regular expression to search for."},"replace_all":{"type":"boolean","description":"Replace every occurrence."}},"required":["path"],"additionalProperties":false}}}`, i)
		}
		buf.WriteString(`],`)
	}
	buf.WriteString(`"messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"`)
	for buf.Len() < target {
		buf.WriteString("analyze this repeated log line and summarize it. ")
	}
	buf.WriteString(`"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue"}]}`)
	return buf.Bytes()
}

// BenchmarkCanonicalSegmentTools measures the uncached canonicalization of 20
// tool definitions, the cost a request pays when its tool set is not cached.
func BenchmarkCanonicalSegmentTools(b *testing.B) {
	tools := gjson.GetBytes(benchChatBody(0, 20), "tools")
	b.ReportAllocs()
	b.SetBytes(int64(len(tools.Raw)))
	for b.Loop() {
		if len(canonicalSegment(tools)) == 0 {
			b.Fatal("empty canonical segment")
		}
	}
}

func BenchmarkDetect(b *testing.B) {
	detector := NewDetector(BuiltinRules(), true)
	for _, size := range []int{1 << 10, 16 << 10, 60 << 10} {
		for _, tools := range []int{0, 20} {
			body := benchChatBody(size, tools)
			snapshot := core.NewRequestSnapshot(
				"POST", "/v1/chat/completions", nil, nil, nil,
				"application/json", body, false, "req-1", nil,
			)
			b.Run(fmt.Sprintf("body=%dKB/tools=%d", len(body)>>10, tools), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if detector.Detect(snapshot, "team/app") == "" {
						b.Fatal("no session id detected")
					}
				}
			})
		}
	}
}
