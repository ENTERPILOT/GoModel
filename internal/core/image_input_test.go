package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBodyHasImageInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "chat image_url part",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
			want: true,
		},
		{
			name: "chat text only",
			body: `{"model":"m","messages":[{"role":"user","content":"describe an image in words"}]}`,
			want: false,
		},
		{
			name: "chat text parts mentioning image",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"\"image\""}]}]}`,
			want: false,
		},
		{
			name: "responses input_image",
			body: `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
			want: true,
		},
		{
			name: "responses string input",
			body: `{"model":"m","input":"draw an image"}`,
			want: false,
		},
		{
			name: "responses tool output with image",
			body: `{"model":"m","input":[{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
			want: true,
		},
		{
			name: "anthropic image block",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
			want: true,
		},
		{
			name: "anthropic tool_result with image",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`,
			want: true,
		},
		{
			name: "image type outside messages is ignored",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"metadata":{"type":"image"}}`,
			want: false,
		},
		{
			name: "invalid json",
			body: `{"messages":[{"type":"image`,
			want: false,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, BodyHasImageInput([]byte(tt.body)))
		})
	}
}

func TestRequestHasImageInput(t *testing.T) {
	t.Parallel()

	assert.False(t, RequestHasImageInput(context.Background()), "no probe attached")
	assert.False(t, RequestHasImageInput(nil)) //nolint:staticcheck // nil context is tolerated on purpose

	calls := 0
	ctx := WithImageInputProbe(context.Background(), func() bool {
		calls++
		return true
	})
	assert.True(t, RequestHasImageInput(ctx))
	assert.True(t, RequestHasImageInput(ctx))
	assert.Equal(t, 1, calls, "the probe runs once per request")

	assert.Equal(t, context.Background(), WithImageInputProbe(context.Background(), nil))
}
