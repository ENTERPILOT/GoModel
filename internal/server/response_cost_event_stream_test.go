package server

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/usage"
)

// A usage object is found wherever the reads split it, in one, two or three
// pieces, and nothing else is taken for one.
func TestUsageObjectFinder(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		want   bool
		broken bool // the value is on another data line: usage.HasUsageObject finds it once the lines are joined
	}{
		{name: "usage object", data: `data: {"choices":[],"usage":{"prompt_tokens":1}}`, want: true},
		{name: "whitespace around the colon", data: "data: {\"usage\" \t: \t{}}", want: true},
		{name: "nested usage object", data: `data: {"response":{"usage":{"input_tokens":1}}}`, want: true},
		{name: "after a string that ends like the name", data: `data: {"a":"usage","usage":{}}`, want: true},
		{name: "value on the next data line", data: "data: {\"usage\":\ndata: {}}", want: true, broken: true},
		{name: "colon on the next data line", data: "data: {\"usage\"\r\ndata: :{}}", want: true, broken: true},
		{name: "null usage", data: `data: {"choices":[],"usage":null}`},
		{name: "usage array", data: `data: {"usage":[{}]}`},
		{name: "usage in a string", data: `data: {"delta":{"content":"\"usage\":{"}}`},
		{name: "usage as a value", data: `data: {"a":"usage"}`},
		{name: "a longer name", data: `data: {"usage_details":{},"my_usage":{},"usages":{}}`},
		{name: "a brace without a colon", data: `data: ["usage"{}]`},
		{name: "two colons", data: `data: {"usage"::{}}`},
		{name: "no usage", data: `data: {"choices":[{"delta":{"content":"Hi"}}]}`},
		{name: "empty", data: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want && !tt.broken, usage.HasUsageObject([]byte(tt.data)), "what the usage observer reads from it whole")
			for i := 0; i <= len(tt.data); i++ {
				for j := i; j <= len(tt.data); j++ {
					var finder usageObjectFinder
					found := false
					for _, piece := range []string{tt.data[:i], tt.data[i:j], tt.data[j:]} {
						found = finder.find([]byte(piece)) || found
					}
					require.Equal(t, tt.want, found, "%q | %q | %q", tt.data[:i], tt.data[i:j], tt.data[j:])
				}
			}
		})
	}
}

// piecesReader returns one piece per Read, the way an upstream's reads split
// a stream.
type piecesReader struct{ pieces []string }

func (r *piecesReader) Read(p []byte) (int, error) {
	if len(r.pieces) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.pieces[0])
	if r.pieces[0] = r.pieces[0][n:]; r.pieces[0] == "" {
		r.pieces = r.pieces[1:]
	}
	return n, nil
}

// An event whose usage the usage reader skips carries no cost, never the cost
// of the event before it, wherever the upstream's reads split that event:
// in its usage name, around the colon or inside the object.
func TestResponseCost_UnreadUsageEventCarriesNoCostHoweverItIsSplit(t *testing.T) {
	const (
		first     = `data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}` + "\n\n"
		firstCost = `data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11,"cost":0.000012}}` + "\n\n"
		skipped   = `data: {"choices":[],"usage" : {"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}` + "\n\n"
		done      = "data: [DONE]\n\n"
	)
	stream := first + skipped + done
	for i := len(first); i <= len(first)+len(skipped); i++ {
		upstream := &piecesReader{pieces: []string{stream[:i], stream[i:]}}
		srv, _ := costGateway(t, costPassthroughProvider(sseHeader(), upstream), returnCosts)

		rec := postJSON(t, srv, passthroughChatPath, passthroughStreamBody)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, firstCost+skipped+done, rec.Body.String(), "split after %q", stream[len(first):i])
	}
}
