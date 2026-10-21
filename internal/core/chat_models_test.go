package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChatUnsupportedReason(t *testing.T) {
	tests := []struct {
		name string
		meta *ModelMetadata
		want string
	}{
		{name: "no metadata", meta: nil, want: ""},
		{name: "nothing described", meta: &ModelMetadata{}, want: ""},
		{name: "chat model", meta: &ModelMetadata{Modes: []string{"chat", "responses"}}, want: ""},
		{name: "responses-only model", meta: &ModelMetadata{Modes: []string{"responses"}}, want: ""},
		{name: "embedding model", meta: &ModelMetadata{Modes: []string{"embedding"}}, want: "serves embedding only, not chat"},
		{name: "decision model", meta: &ModelMetadata{Categories: []ModelCategory{CategoryUtility}}, want: "is a decision model (it answers /v1/decisions or /v1/systemone), not a chat model"},
		{name: "moderation model", meta: &ModelMetadata{Modes: []string{"moderation"}, Categories: []ModelCategory{CategoryUtility}}, want: "serves moderation only, not chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ChatUnsupportedReason(tt.meta))
		})
	}
}
