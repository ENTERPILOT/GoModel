// Command mockmcp serves two deterministic MCP upstream servers ("alpha" and
// "beta") over streamable HTTP for the release E2E curl matrix. It is built
// with the same MCP SDK the gateway uses, so the wire format matches real
// upstreams.
//
//	alpha (/alpha): tools echo, add; prompt greeting; resource mock://alpha/info;
//	                instructions "MOCKMCP_ALPHA_INSTRUCTIONS". When
//	                MOCK_MCP_TOKEN is set, /alpha requires the X-Mock-Token
//	                header to match, so header forwarding and the admin "***"
//	                secret round-trip can be exercised end to end.
//	beta  (/beta):  tools search, fetch.
//
// PORT selects the listen port (default 18090). GET /healthz reports liveness.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func echoTool(name string) (*sdk.Tool, sdk.ToolHandler) {
	tool := &sdk.Tool{
		Name:        name,
		Description: "mockmcp " + name,
		InputSchema: map[string]any{"type": "object"},
	}
	handler := func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: name + ":" + string(req.Params.Arguments)}},
		}, nil
	}
	return tool, handler
}

func newAlpha() *sdk.Server {
	server := sdk.NewServer(
		&sdk.Implementation{Name: "alpha", Version: "e2e"},
		&sdk.ServerOptions{Instructions: "MOCKMCP_ALPHA_INSTRUCTIONS"},
	)
	server.AddTool(echoTool("echo"))
	server.AddTool(echoTool("add"))
	server.AddPrompt(&sdk.Prompt{Name: "greeting", Description: "mockmcp greeting prompt"},
		func(_ context.Context, _ *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{
				Description: "mockmcp greeting prompt",
				Messages: []*sdk.PromptMessage{
					{Role: "user", Content: &sdk.TextContent{Text: "MOCKMCP_GREETING_OK"}},
				},
			}, nil
		})
	server.AddResource(&sdk.Resource{URI: "mock://alpha/info", Name: "info", MIMEType: "text/plain"},
		func(_ context.Context, _ *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{
				Contents: []*sdk.ResourceContents{
					{URI: "mock://alpha/info", MIMEType: "text/plain", Text: "MOCKMCP_ALPHA_RESOURCE_OK"},
				},
			}, nil
		})
	return server
}

func newBeta() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "beta", Version: "e2e"}, nil)
	server.AddTool(echoTool("search"))
	server.AddTool(echoTool("fetch"))
	return server
}

func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("X-Mock-Token") != token {
			http.Error(w, "missing or wrong X-Mock-Token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "18090"
	}
	token := os.Getenv("MOCK_MCP_TOKEN")

	alpha := newAlpha()
	beta := newBeta()

	mux := http.NewServeMux()
	mux.Handle("/alpha", requireToken(token,
		sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return alpha }, nil)))
	mux.Handle("/beta",
		sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return beta }, nil))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	log.Printf("mockmcp listening on :%s (alpha token required: %v)", port, token != "")
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
