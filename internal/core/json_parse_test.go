package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestParseJSONNoCopy(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "object", body: `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"x":{"a":1}}`},
		{name: "escaped strings", body: `{"model":"gpt-4o","k\"ey":"v\nw"}`},
		{name: "array", body: `[1,"two",{"three":3}]`},
		{name: "scalar", body: `"just a string"`},
		{name: "empty", body: ``},
		{name: "invalid", body: `{"model":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := gjson.ParseBytes([]byte(tt.body))
			got := ParseJSONNoCopy([]byte(tt.body))
			assert.Equal(t, want.Type, got.Type)
			assert.Equal(t, want.Raw, got.Raw)
			assert.Equal(t, want.String(), got.String())
			assert.Equal(t, want.Get("model").String(), got.Get("model").String())
			assert.Equal(t, want.IsObject(), got.IsObject())
		})
	}
}

func TestParseJSONNoCopyDoesNotCopyTheInput(t *testing.T) {
	body := []byte(`{"model":"gpt-4o-mini","content":"` + strings.Repeat("x", 64<<10) + `"}`)
	allocs := testing.AllocsPerRun(10, func() {
		if !ParseJSONNoCopy(body).IsObject() {
			panic("expected an object")
		}
	})
	assert.Zero(t, allocs)
}
