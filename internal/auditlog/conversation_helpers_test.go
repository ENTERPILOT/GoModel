package auditlog

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestExtractStringField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    any
		key  string
		want string
	}{
		{
			name: "map payload",
			v: map[string]any{
				"id": " resp_1 ",
			},
			key:  "id",
			want: "resp_1",
		},
		{
			name: "bson m payload",
			v: bson.M{
				"id": " resp_2 ",
			},
			key:  "id",
			want: "resp_2",
		},
		{
			name: "bson d payload",
			v: bson.D{
				{Key: "id", Value: " resp_3 "},
			},
			key:  "id",
			want: "resp_3",
		},
		{
			name: "missing key",
			v: bson.D{
				{Key: "other", Value: "x"},
			},
			key:  "id",
			want: "",
		},
		{
			name: "non string value",
			v: bson.M{
				"id": 123,
			},
			key:  "id",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractStringField(tc.v, tc.key); got != tc.want {
				t.Fatalf("extractStringField() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractConversationIDsFromBSONBodies(t *testing.T) {
	t.Parallel()

	entry := &LogEntry{
		Data: &LogData{
			RequestBody: bson.D{
				{Key: "previous_response_id", Value: " resp_prev "},
			},
			ResponseBody: bson.D{
				{Key: "id", Value: " resp_cur "},
			},
		},
	}

	if got := extractPreviousResponseID(entry); got != "resp_prev" {
		t.Fatalf("extractPreviousResponseID() = %q, want %q", got, "resp_prev")
	}
	if got := extractResponseID(entry); got != "resp_cur" {
		t.Fatalf("extractResponseID() = %q, want %q", got, "resp_cur")
	}
}
