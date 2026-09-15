package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsCredentialHeader(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "authorization", want: true},
		{name: "Authorization", want: true},
		{name: " X-API-Key ", want: true},
		{name: "Set-Cookie", want: true},
		{name: "x-gomodel-key", want: true},
		{name: "X-Team", want: false},
		{name: "Content-Type", want: false},
		{name: "", want: false},
	}
	for _, tt := range tests {
		got := IsCredentialHeader(tt.name)
		assert.Equal(t, tt.want, got, "IsCredentialHeader(%q)", tt.name)
	}
}
