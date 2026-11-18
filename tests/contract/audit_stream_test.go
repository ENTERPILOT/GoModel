//go:build contract

package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// auditUnreadableStreams lists recorded streams the audit log never sees, and
// why. Anything else recorded under testdata must rebuild into a reply.
var auditUnreadableStreams = map[string]string{
	"gemini/native_chat_completion_stream.txt": "Gemini's native stream is translated to chat chunks before it is observed, and Gemini has no passthrough route",
}

type auditStreamLogger struct {
	entries []*auditlog.LogEntry
}

func (l *auditStreamLogger) Write(entry *auditlog.LogEntry) { l.entries = append(l.entries, entry) }
func (l *auditStreamLogger) Config() auditlog.Config {
	return auditlog.Config{Enabled: true, LogBodies: true}
}
func (l *auditStreamLogger) Close() error { return nil }

// auditPathForStream is the audit path the gateway records for a stream of
// this kind, which is all the observer is told about its dialect.
func auditPathForStream(fixture string) string {
	switch name := filepath.Base(fixture); {
	case strings.HasPrefix(name, "responses"):
		return "/v1/responses"
	case strings.HasPrefix(name, "messages"):
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

// replyOf collects the assistant's text and tool calls from a rebuilt
// response body, whichever dialect it is in. A tool call counts by its
// arguments: a string in the OpenAI dialects, an "input" object in
// Anthropic's, where even an empty one is a call.
func replyOf(body any) string {
	var reply strings.Builder
	switch value := body.(type) {
	case map[string]any:
		for key, item := range value {
			if key == "input" && item != nil {
				raw, _ := json.Marshal(item)
				reply.Write(raw)
				continue
			}
			if text, ok := item.(string); ok {
				if key == "content" || key == "text" || key == "arguments" {
					reply.WriteString(text)
				}
				continue
			}
			reply.WriteString(replyOf(item))
		}
	case []any:
		for _, item := range value {
			reply.WriteString(replyOf(item))
		}
	}
	return reply.String()
}

// Every stream a provider was recorded to send must rebuild, in the audit
// log, into the reply it carried. A new recording joins this test by being
// added to testdata, so a dialect the observer cannot read fails here instead
// of logging empty replies in production.
func TestAuditLogRebuildsRecordedStreams(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join(testdataDir, "*", "*stream*.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)

	for _, fullPath := range fixtures {
		fixture, err := filepath.Rel(testdataDir, fullPath)
		require.NoError(t, err)
		fixture = filepath.ToSlash(fixture)

		t.Run(fixture, func(t *testing.T) {
			if reason, ok := auditUnreadableStreams[fixture]; ok {
				t.Skip(reason)
			}

			logger := &auditStreamLogger{}
			entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
			observer := auditlog.NewStreamLogObserver(logger, entry, auditPathForStream(fixture))
			stream := streaming.NewObservedSSEStream(io.NopCloser(bytes.NewReader(loadGoldenFileRaw(t, fixture))), observer)
			_, err := io.Copy(io.Discard, stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			require.Len(t, logger.entries, 1)

			raw, err := json.Marshal(logger.entries[0].Data.ResponseBody)
			require.NoError(t, err)
			var rebuilt any
			require.NoError(t, json.Unmarshal(raw, &rebuilt))
			require.NotEmpty(t, replyOf(rebuilt), "the audit log rebuilt no reply from this stream: %s", raw)

			compareGoldenJSON(t, filepath.Join("audit", goldenPathForFixture(fixture)), rebuilt)
		})
	}
}
