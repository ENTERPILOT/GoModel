package minimax

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
)

func TestFinishCarryFrame_ChoiceNotMapReturnsNil(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	assert.Nil(t, ts.finishCarryFrame(json.RawMessage(`"a string"`)))
}

func TestFinishSplit_ChoiceNotMapReturnsUnchanged(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got, frames := ts.finishSplit(json.RawMessage(`"a string"`))
	assert.Nil(t, frames)
	assert.Equal(t, json.RawMessage(`"a string"`), got)
}

func TestFinishSplit_NoParserReturnsUnchanged(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	raw := json.RawMessage(`{"index":3,"delta":{"content":"x"},"finish_reason":"stop"}`)
	got, frames := ts.finishSplit(raw)
	assert.Nil(t, frames, "a finishing choice with no parser entry cannot have parked carry")
	assert.Equal(t, raw, got)
}
