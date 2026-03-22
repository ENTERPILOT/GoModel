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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Provider configurations
var providerConfigs = map[string]struct {
	baseURL     string
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
}

var providerCapabilities = map[string]map[string]bool{
	"openai": {
		"responses": true,
	},
	"anthropic": {
		"responses": false,
	},
	"gemini": {
		"responses": false,
	},
	"groq": {
		"responses": false,
	},
	"xai": {
		"responses": true,
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

func main() {
	provider := flag.String("provider", "openai", "Provider to test (openai, anthropic, gemini, groq, xai)")
	endpoint := flag.String("endpoint", "chat", "Endpoint to test (chat, chat_stream, models, responses, responses_stream)")
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

	eConfig, ok := endpointConfigs[*endpoint]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: unknown endpoint %q\n", *endpoint)
		os.Exit(1)
	}
	if endpointRequiresResponsesCapability(*endpoint) && !providerSupportsResponses(*provider) {
		fmt.Fprintf(os.Stderr, "Error: provider %q is missing responses capability (/v1/responses)\n", *provider)
		os.Exit(1)
	}

	apiKey := os.Getenv(pConfig.envKey)
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "Error: %s environment variable is required\n", pConfig.envKey)
		os.Exit(1)
	}

	// Build request body
	var bodyReader io.Reader
	if eConfig.requestBody != nil {
		reqBody := eConfig.requestBody

		// Override model if specified
		if *model != "" {
			reqBody["model"] = *model
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

	// Build URL
	url := pConfig.baseURL + eConfig.path

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
