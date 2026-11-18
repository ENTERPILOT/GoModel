package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestModelObject(t *testing.T) {
	tests := []struct {
		name   string
		object string
		want   string
	}{
		{name: "blank defaults to model", object: "", want: "model"},
		{name: "whitespace defaults to model", object: "  ", want: "model"},
		{name: "reported object is trimmed", object: " model ", want: "model"},
		{name: "other object is kept", object: "embedding", want: "embedding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ModelObject(tt.object))
		})
	}
}

func TestModelList(t *testing.T) {
	entries := []string{"a", "", "  ", "skip", "b"}
	got := ModelList(entries, func(id string) core.Model {
		if id == "skip" {
			return core.Model{}
		}
		return core.Model{ID: id, Object: "model"}
	})

	assert.Equal(t, &core.ModelsResponse{
		Object: "list",
		Data:   []core.Model{{ID: "a", Object: "model"}, {ID: "b", Object: "model"}},
	}, got)
}

func TestModelListEmpty(t *testing.T) {
	got := ModelList[string](nil, func(string) core.Model { return core.Model{ID: "x"} })

	assert.Equal(t, "list", got.Object)
	assert.NotNil(t, got.Data)
	assert.Empty(t, got.Data)
}
