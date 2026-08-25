// Package main provides a CLI tool to record real API responses for contract tests.
// Usage:
//
//	OPENAI_API_KEY=sk-xxx go run ./cmd/recordapi \
//	  -provider=openai \
//	  -endpoint=chat \
//	  -output=tests/contract/testdata/openai/chat_completion.json
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

const (
	oracleDefaultModel             = "openai.gpt-oss-120b"
	kimicodeDefaultChatModel       = "kimi-for-coding"
	kimicodeDefaultEmbeddingsModel = "bge_m3_embed"
)

// Provider configurations
var providerConfigs = map[string]struct {
	baseURL     string
	baseURLEnv  string
	envKey      string
	authHeader  string
	contentType string
}{
	"openai": {
		baseURL:     "https://api.openai.com",
		envKey:      "OPENAI_API_KEY",
		authHeader:  "Authorization",
		contentType: "application/json",
	},
	"anthropic": {
		baseURL:     "https://api.anthropic.com",
		envKey:      "ANTHROPIC_API_KEY",
		authHeader:  "x-api-key",
		contentType: "application/json",
	},
	"gemini": {
		baseURL:     "https://generativelanguage.googleapis.com/v1beta/openai",
		envKey:      "GEMINI_API_KEY",
		authHeader:  "Authorization",
		contentType: "application/json",
	},
	// gemini-native records Gemini's native API shapes (generateContent,
	// batchEmbedContents); model IDs live in the path, not the body.
	"gemini-native": {
		baseURL:     "https://generativelanguage.googleapis.com/v1beta",
		envKey:      "GEMINI_API_KEY",
		authHeader:  "x-goog-api-key",
		contentType: "application/json",
	},
	"groq": {
		baseURL:     "https://api.groq.com/openai",
		envKey:      "GROQ_API_KEY",
		authHeader:  "Authorization",
		contentType: "application/json",
	},
	"xai": {
		baseURL:     "https://api.x.ai",
		envKey:      "XAI_API_KEY",
		authHeader:  "Authorization",
		contentType: "application/json",
	},
	"kimicode": {
		baseURL:     "https://api.kimi.com/coding",
		envKey:      "KIMICODE_API_KEY",
		authHeader:  "Authorization",
		contentType: "application/json",
	},
	"oracle": {
		baseURLEnv:  "ORACLE_BASE_URL",
		envKey:      "ORACLE_API_KEY",
		authHeader:  "Authorization",
		contentType: "application/json",
	},
}

// Endpoint configurations
var endpointConfigs = map[string]struct {
	path        string
	method      string
	requestBody map[string]any
}{
	"chat": {
		path:   "/v1/chat/completions",
		method: http.MethodPost,
		requestBody: map[string]any{
			"model": "gpt-4o-mini",
			"messages": []map[string]string{
				{"role": "user", "content": "Say 'Hello, World!' and nothing else."},
			},
			"max_tokens": 50,
		},
	},
	"chat_stream": {
		path:   "/v1/chat/completions",
		method: http.MethodPost,
		requestBody: map[string]any{
			"model": "gpt-4o-mini",
			"messages": []map[string]string{
				{"role": "user", "content": "Say 'Hello, World!' and nothing else."},
			},
			"max_tokens": 50,
			"stream":     true,
		},
	},
	"models": {
		path:   "/v1/models",
		method: http.MethodGet,
	},
	"responses": {
		path:   "/v1/responses",
		method: http.MethodPost,
		requestBody: map[string]any{
			"model": "gpt-4o-mini",
			"input": "Say 'Hello, World!' and nothing else.",
		},
	},
	"responses_stream": {
		path:   "/v1/responses",
		method: http.MethodPost,
		requestBody: map[string]any{
			"model":  "gpt-4o-mini",
			"input":  "Say 'Hello, World!' and nothing else.",
			"stream": true,
		},
	},
	"embeddings": {
		path:   "/embeddings",
		method: http.MethodPost,
		requestBody: map[string]any{
			"model": "text-embedding-3-small",
			"input": "hello world",
		},
	},
	// Native Gemini endpoints (gemini-native provider only).
	"generate_content": {
		path:   "/models/gemini-2.5-flash:generateContent",
		method: http.MethodPost,
		requestBody: map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": "Say 'Hello, World!' and nothing else."}}},
			},
			"generationConfig": map[string]any{"maxOutputTokens": 50},
		},
	},
	"generate_content_stream": {
		path:   "/models/gemini-2.5-flash:streamGenerateContent?alt=sse",
		method: http.MethodPost,
		requestBody: map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": "Say 'Hello, World!' and nothing else."}}},
			},
			"generationConfig": map[string]any{"maxOutputTokens": 50},
		},
	},
	"image_generate_content": {
		path:   "/models/gemini-2.5-flash-image:generateContent",
		method: http.MethodPost,
		requestBody: map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": "A tiny solid red square on a white background"}}},
			},
			"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}},
		},
	},
	"batch_embed_contents": {
		path:   "/models/gemini-embedding-001:batchEmbedContents",
		method: http.MethodPost,
		requestBody: map[string]any{
			// Small output dimensionality keeps the recorded fixture readable.
			"requests": []map[string]any{
				{
					"model":                "models/gemini-embedding-001",
					"content":              map[string]any{"parts": []map[string]any{{"text": "hello world"}}},
					"outputDimensionality": 8,
				},
				{
					"model":                "models/gemini-embedding-001",
					"content":              map[string]any{"parts": []map[string]any{{"text": "second input"}}},
					"outputDimensionality": 8,
				},
			},
		},
	},
}

// providerExclusiveEndpoints restricts endpoints whose paths only exist on a
// single provider's API surface.
var providerExclusiveEndpoints = map[string]string{
	"generate_content":        "gemini-native",
	"generate_content_stream": "gemini-native",
	"image_generate_content":  "gemini-native",
	"batch_embed_contents":    "gemini-native",
}

// providerAllowedEndpoints restricts providers whose base URL cannot serve the
// generic OpenAI-style endpoint paths; only the listed endpoints are valid.
var providerAllowedEndpoints = map[string]map[string]bool{
	"gemini-native": {
		"generate_content":        true,
		"generate_content_stream": true,
		"image_generate_content":  true,
		"batch_embed_contents":    true,
		"models":                  true,
	},
}

var providerCapabilities = map[string]map[string]bool{
	"openai": {
		"responses":  true,
		"embeddings": true,
	},
	"anthropic": {
		"responses":  false,
		"embeddings": false,
	},
	"gemini": {
		"responses":  false,
		"embeddings": true,
	},
	"groq": {
		"responses":  false,
		"embeddings": true,
	},
	"xai": {
		"responses":  true,
		"embeddings": false,
	},
	"kimicode": {
		"responses":  false,
		"embeddings": true,
	},
	"oracle": {
		"responses":  true,
		"embeddings": false,
	},
}

var providerDefaultModels = map[string]map[string]string{
	"oracle": {
		"chat":             oracleDefaultModel,
		"chat_stream":      oracleDefaultModel,
		"responses":        oracleDefaultModel,
		"responses_stream": oracleDefaultModel,
	},
	"kimicode": {
		"chat":        kimicodeDefaultChatModel,
		"chat_stream": kimicodeDefaultChatModel,
		"embeddings":  kimicodeDefaultEmbeddingsModel,
	},
}

// providerEndpointPathOverrides lets a provider replace the generic endpoint
// path when the upstream layout differs from the default OpenAI-style routes.
// Gemini is intentionally omitted because its base URL already includes the
// OpenAI-compatible /v1beta/openai prefix, so the embeddings path remains
// the generic /embeddings suffix.
var providerEndpointPathOverrides = map[string]map[string]string{
	"openai": {
		"embeddings": "/v1/embeddings",
	},
	"gemini-native": {
		"models": "/models",
	},
	"groq": {
		"embeddings": "/v1/embeddings",
	},
	"kimicode": {
		"embeddings": "/v1/embeddings",
	},
}

func endpointRequiresResponsesCapability(endpoint string) bool {
	return endpoint == "responses" || endpoint == "responses_stream"
}

func providerSupportsResponses(provider string) bool {
	capabilities, ok := providerCapabilities[provider]
	if !ok {
		return false
	}
	return capabilities["responses"]
}

func endpointRequiresEmbeddingsCapability(endpoint string) bool {
	return endpoint == "embeddings"
}

func providerSupportsEmbeddings(provider string) bool {
	capabilities, ok := providerCapabilities[provider]
	if !ok {
		return false
	}
	return capabilities["embeddings"]
}

func main() {
	provider := flag.String("provider", "openai", "Provider to test (openai, anthropic, gemini, gemini-native, groq, xai, kimicode, oracle)")
	endpoint := flag.String("endpoint", "chat", "Endpoint to test (chat, chat_stream, models, responses, responses_stream, embeddings, generate_content, generate_content_stream, image_generate_content, batch_embed_contents)")
	output := flag.String("output", "", "Output file path (required)")
	model := flag.String("model", "", "Override model in request")
	flag.Parse()

	if *output == "" {
		fmt.Fprintln(os.Stderr, "Error: -output flag is required")
		flag.Usage()
		os.Exit(1)
	}

	pConfig, ok := providerConfigs[*provider]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: unknown provider %q\n", *provider)
		os.Exit(1)
	}

	baseURL := pConfig.baseURL
	if pConfig.baseURLEnv != "" {
		baseURL = os.Getenv(pConfig.baseURLEnv)
		if baseURL == "" {
			fmt.Fprintf(os.Stderr, "Error: %s environment variable is required\n", pConfig.baseURLEnv)
			os.Exit(1)
		}
	}

	eConfig, ok := endpointConfigs[*endpoint]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: unknown endpoint %q\n", *endpoint)
		os.Exit(1)
	}
	if endpointRequiresResponsesCapability(*endpoint) && !providerSupportsResponses(*provider) {
		fmt.Fprintf(os.Stderr, "Error: provider %q is missing responses capability (/v1/responses)\n", *provider)
		os.Exit(1)
	}
	if endpointRequiresEmbeddingsCapability(*endpoint) && !providerSupportsEmbeddings(*provider) {
		fmt.Fprintf(os.Stderr, "Error: provider %q is missing embeddings capability (/embeddings)\n", *provider)
		os.Exit(1)
	}
	if required, ok := providerExclusiveEndpoints[*endpoint]; ok && *provider != required {
		fmt.Fprintf(os.Stderr, "Error: endpoint %q is only recordable for provider %q\n", *endpoint, required)
		os.Exit(1)
	}
	if allowed, ok := providerAllowedEndpoints[*provider]; ok && !allowed[*endpoint] {
		fmt.Fprintf(os.Stderr, "Error: provider %q does not serve endpoint %q\n", *provider, *endpoint)
		os.Exit(1)
	}

	apiKey := os.Getenv(pConfig.envKey)
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "Error: %s environment variable is required\n", pConfig.envKey)
		os.Exit(1)
	}

	// The endpoint path is resolved before the body: native Gemini model
	// overrides rewrite both the path and nested body fields.
	endpointPath := eConfig.path
	if overrides, ok := providerEndpointPathOverrides[*provider]; ok {
		if override, ok := overrides[*endpoint]; ok {
			endpointPath = override
		}
	}
	if *provider == "gemini-native" {
		endpointPath = applyNativeGeminiModelOverride(endpointPath, eConfig.requestBody, *model)
	}

	// Build request body
	var bodyReader io.Reader
	if eConfig.requestBody != nil {
		reqBody := eConfig.requestBody

		// Provider-specific defaults override the generic fixture model when no
		// explicit model override is supplied (e.g., Oracle's OCI-hosted IDs or
		// Kimicode's provider-specific chat/embeddings models). Native Gemini
		// bodies carry no top-level model — the ID lives in the path (and in
		// requests[].model for batches), rewritten below.
		if *provider == "gemini-native" {
			// handled by applyNativeGeminiModelOverride
		} else if *model != "" {
			reqBody["model"] = *model
		} else if providerDefaults, ok := providerDefaultModels[*provider]; ok {
			if defaultModel, ok := providerDefaults[*endpoint]; ok {
				reqBody["model"] = defaultModel
			}
		}

		// Adjust request for different providers
		if *provider == "anthropic" {
			reqBody = adjustForAnthropic(reqBody)
		}

		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling request body: %v\n", err)
			os.Exit(1)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	url := baseURL + endpointPath

	// Create request
	req, err := http.NewRequest(eConfig.method, url, bodyReader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("Content-Type", pConfig.contentType)

	// Add auth header (except for Gemini which uses query param)
	if pConfig.authHeader != "" {
		if pConfig.authHeader == "Authorization" {
			req.Header.Set(pConfig.authHeader, "Bearer "+apiKey)
		} else {
			req.Header.Set(pConfig.authHeader, apiKey)
		}
	}

	// Add Anthropic-specific headers
	if *provider == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	// Send request
	client := &http.Client{Timeout: 60 * time.Second}
	fmt.Printf("Sending request to %s %s...\n", eConfig.method, url)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error sending request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	fmt.Printf("Response status: %d %s\n", resp.StatusCode, resp.Status)

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading response: %v\n", err)
		os.Exit(1)
	}

	// Handle streaming responses differently
	if strings.HasSuffix(*endpoint, "_stream") {
		if err := writeStreamOutput(*output, body); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Streaming response saved to %s\n", *output)
		return
	}

	// Pretty print JSON
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, body, "", "  "); err != nil {
		// If it's not valid JSON, write raw
		if err := writeOutput(*output, body); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Raw response saved to %s\n", *output)
		return
	}

	if err := writeOutput(*output, prettyJSON.Bytes()); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Response saved to %s\n", *output)

	// Print response summary
	var respMap map[string]any
	if err := json.Unmarshal(body, &respMap); err == nil {
		if id, ok := respMap["id"].(string); ok {
			fmt.Printf("Response ID: %s\n", id)
		}
		if model, ok := respMap["model"].(string); ok {
			fmt.Printf("Model: %s\n", model)
		}
	}
}

// applyNativeGeminiModelOverride points a native Gemini recording at another
// model: the ID lives in the endpoint path (/models/{id}:{verb}) and, for
// batch embeddings, in each requests[].model value — never as a top-level
// body field.
func applyNativeGeminiModelOverride(endpointPath string, reqBody map[string]any, model string) string {
	if model == "" {
		return endpointPath
	}
	if rest, ok := strings.CutPrefix(endpointPath, "/models/"); ok {
		if _, verb, found := strings.Cut(rest, ":"); found {
			endpointPath = "/models/" + model + ":" + verb
		}
	}
	if reqBody == nil {
		return endpointPath
	}
	if requests, ok := reqBody["requests"].([]map[string]any); ok {
		for _, request := range requests {
			request["model"] = "models/" + model
		}
	}
	return endpointPath
}

// adjustForAnthropic converts OpenAI-style request to Anthropic format
func adjustForAnthropic(req map[string]any) map[string]any {
	result := make(map[string]any)

	// Copy model
	if model, ok := req["model"].(string); ok {
		result["model"] = model
	}

	// Convert max_tokens
	if maxTokens, ok := req["max_tokens"].(int); ok {
		result["max_tokens"] = maxTokens
	} else {
		result["max_tokens"] = 1024 // Default for Anthropic
	}

	// Convert messages
	if messages, ok := req["messages"].([]map[string]string); ok {
		result["messages"] = messages
	}

	return result
}

// writeOutput writes data to the output file, creating directories as needed.
func writeOutput(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// writeStreamOutput writes streaming response data to a text file.
func writeStreamOutput(path string, data []byte) error {
	// For streaming responses, save as-is (SSE format)
	return writeOutput(path, data)
}
