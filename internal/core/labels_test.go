package core

import (
	"reflect"
	"testing"
)

func TestMergeLabels(t *testing.T) {
	tests := []struct {
		name string
		sets [][]string
		want []string
	}{
		{
			name: "nil input returns nil",
			sets: nil,
			want: nil,
		},
		{
			name: "empty sets return nil",
			sets: [][]string{nil, {}},
			want: nil,
		},
		{
			name: "single set is trimmed and deduplicated",
			sets: [][]string{{" prod ", "prod", "", "  ", "batch"}},
			want: []string{"prod", "batch"},
		},
		{
			name: "sets merge in order with first occurrence winning",
			sets: [][]string{{"header-a", "shared"}, {"shared", "key-b"}},
			want: []string{"header-a", "shared", "key-b"},
		},
		{
			name: "whitespace-only sets return nil",
			sets: [][]string{{"", "   "}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeLabels(tt.sets...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MergeLabels(%v) = %v, want %v", tt.sets, got, tt.want)
			}
		})
	}
}
