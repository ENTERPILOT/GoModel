package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/users"
)

// A Messages request served by its path's default model is forwarded
// natively like one that names the model: history only Anthropic understands
// (server tool use) cannot take the translated path, so the resolved model
// is written into the client's body, whose other bytes reach Anthropic as
// sent.
func TestDefaultModelKeepsNativeMessagesForwarding(t *testing.T) {
	const history = `"max_tokens":64,"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search"},` +
		`{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]},` +
		`{"role":"user","content":"and?"}]`
	rows := []users.User{{UserPath: "/claude", DefaultModel: "anthropic/claude-sonnet-4-5"}}
	for name, body := range map[string]string{
		"no model member":  `{` + history + `}`,
		"an empty model":   `{"model":"",` + history + `}`,
		"the model itself": `{"model":"anthropic/claude-sonnet-4-5",` + history + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpstreamFixture(t, rows, nil)
			rec := f.post(t, "/claude", "/v1/messages", body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, `{"model":"claude-sonnet-4-5",`+history+`}`, string(f.upstreams["anthropic"].Last(t).Body))
		})
	}
}
