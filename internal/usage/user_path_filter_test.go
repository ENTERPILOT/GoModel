package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageUserPathSubtreePattern(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		want     string
	}{
		{
			name:     "root matches full subtree",
			userPath: "/",
			want:     "/%",
		},
		{
			name:     "nested path appends descendant wildcard",
			userPath: "/team/a",
			want:     "/team/a/%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, usageUserPathSubtreePattern(tt.userPath))
		})
	}
}
