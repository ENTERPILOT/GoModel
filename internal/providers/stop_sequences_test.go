package providers

import (
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// A client cannot know each provider's stop-sequence limit, so an over-long
// list is truncated rather than rejected upstream. Everything else about the
// request, including other extra fields, must survive untouched.
func TestCapStopSequences(t *testing.T) {
	cases := []struct {
		name  string
		stop  string
		limit int
		want  string
	}{
		{name: "within the limit", stop: `["a","b"]`, limit: 4, want: `["a","b"]`},
		{name: "at the limit", stop: `["a","b","c","d"]`, limit: 4, want: `["a","b","c","d"]`},
		{name: "over the limit", stop: `["a","b","c","d","e","f"]`, limit: 4, want: `["a","b","c","d"]`},
		{name: "bare string", stop: `"a"`, limit: 4, want: `"a"`},
		{name: "no limit", stop: `["a","b","c","d","e"]`, limit: 0, want: `["a","b","c","d","e"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
				"stop": json.RawMessage(tc.stop),
				"seed": json.RawMessage("7"),
			})
			if err != nil {
				t.Fatalf("MergeUnknownJSONFields: %v", err)
			}
			req := &core.ChatRequest{Model: "gpt-4.1-mini", ExtraFields: extra}

			adapted, err := CapStopSequences(req, tc.limit)
			if err != nil {
				t.Fatalf("CapStopSequences: %v", err)
			}
			if got := string(adapted.ExtraFields.Lookup("stop")); got != tc.want {
				t.Errorf("stop = %s, want %s", got, tc.want)
			}
			if got := string(adapted.ExtraFields.Lookup("seed")); got != "7" {
				t.Errorf("seed = %s, want 7", got)
			}
			if got := string(req.ExtraFields.Lookup("stop")); got != tc.stop {
				t.Errorf("original request mutated: stop = %s, want %s", got, tc.stop)
			}
		})
	}
}

func TestCapStopSequencesWithoutStopField(t *testing.T) {
	req := &core.ChatRequest{Model: "gpt-4.1-mini"}
	adapted, err := CapStopSequences(req, 4)
	if err != nil {
		t.Fatalf("CapStopSequences: %v", err)
	}
	if adapted != req {
		t.Fatal("request without a stop field should be returned unchanged")
	}
	if adapted, err = CapStopSequences(nil, 4); err != nil || adapted != nil {
		t.Fatalf("CapStopSequences(nil) = (%v, %v), want (nil, nil)", adapted, err)
	}
}
