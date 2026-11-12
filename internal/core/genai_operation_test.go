package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenAIOperation(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		set  []string
		want string
	}{
		{name: "no record", ctx: context.Background(), set: []string{"chat"}, want: ""},
		{name: "nothing recorded", ctx: WithGenAIOperation(context.Background()), want: ""},
		{name: "last call wins", ctx: WithGenAIOperation(context.Background()), set: []string{"chat", "generate_content"}, want: "generate_content"},
		{name: "empty operation is ignored", ctx: WithGenAIOperation(context.Background()), set: []string{"chat", ""}, want: "chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := GenAIOperationFromContext(tt.ctx)
			for _, name := range tt.set {
				record.Set(name)
			}
			assert.Equal(t, tt.want, GenAIOperationFromContext(tt.ctx).Name())
		})
	}
}
