package bedrockmantle

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	defaultRegion = "us-east-1"
	modeAuto      = "auto"
	modeOpenAI    = "openai"
	modeStandard  = "standard"
)

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-\d+$`)

type endpointConfig struct {
	baseURL string
	region  string
	mode    string
}

func resolveEndpoint(baseURL, apiMode string) (endpointConfig, error) {
	mode := strings.ToLower(strings.TrimSpace(apiMode))
	if mode == "" {
		mode = modeAuto
	}
	if mode != modeAuto && mode != modeOpenAI && mode != modeStandard {
		return endpointConfig{}, fmt.Errorf("api_mode must be auto, openai, or standard, got %q", apiMode)
	}

	value := strings.TrimSpace(baseURL)
	region := ""
	if value == "" {
		region = configuredRegion()
		value = mantleEndpoint(region)
	} else if !strings.Contains(value, "://") {
		region = value
		if !regionPattern.MatchString(region) {
			return endpointConfig{}, fmt.Errorf("invalid AWS region %q", region)
		}
		value = mantleEndpoint(region)
	} else {
		normalized, inferredRegion, err := normalizeEndpointURL(value)
		if err != nil {
			return endpointConfig{}, err
		}
		value = normalized
		region = inferredRegion
		if region == "" {
			region = configuredRegion()
		}
	}
	if !regionPattern.MatchString(region) {
		return endpointConfig{}, fmt.Errorf("invalid AWS region %q", region)
	}

	return endpointConfig{baseURL: value, region: region, mode: mode}, nil
}

func configuredRegion() string {
	for _, name := range []string{"BEDROCK_MANTLE_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		if region := strings.TrimSpace(os.Getenv(name)); region != "" {
			return region
		}
	}
	return defaultRegion
}

func mantleEndpoint(region string) string {
	return "https://bedrock-mantle." + region + ".api.aws"
}

func normalizeEndpointURL(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("invalid Bedrock Mantle base URL %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("base URL for Bedrock Mantle must use http or https, got %q", u.Scheme)
	}

	u.RawQuery = ""
	u.Fragment = ""
	u.Path = stripAPISuffix(u.Path)
	u.RawPath = ""

	region := ""
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) >= 4 && parts[0] == "bedrock-mantle" && parts[len(parts)-2] == "api" && parts[len(parts)-1] == "aws" {
		region = parts[1]
	}
	return strings.TrimRight(u.String(), "/"), region, nil
}

func stripAPISuffix(path string) string {
	path = strings.TrimRight(path, "/")
	for _, suffix := range []string{
		"/openai/v1/chat/completions",
		"/openai/v1/embeddings",
		"/openai/v1/responses",
		"/v1/chat/completions",
		"/v1/embeddings",
		"/v1/responses",
		"/openai/v1",
		"/v1/models",
		"/v1",
	} {
		if before, ok := strings.CutSuffix(path, suffix); ok {
			return before
		}
	}
	return path
}
