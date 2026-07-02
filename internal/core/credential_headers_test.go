package core

import "testing"

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
		if got := IsCredentialHeader(tt.name); got != tt.want {
			t.Errorf("IsCredentialHeader(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
