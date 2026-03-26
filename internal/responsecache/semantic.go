package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/embedding"
)

// SemanticCacheMiddleware implements the vector-similarity response cache layer.
// It is the second cache layer, consulted only after an exact-match miss.
type semanticCacheMiddleware struct {
	embedder         embedding.Embedder
	store            VecStore
	cfg              config.SemanticCacheConfig
	embedderIdentity string
	wg               sync.WaitGroup
}

func newSemanticCacheMiddleware(emb embedding.Embedder, store VecStore, cfg config.SemanticCacheConfig) *semanticCacheMiddleware {
	e := cfg.Embedder
	return &semanticCacheMiddleware{
		embedder:         emb,
		store:            store,
		cfg:              cfg,
		embedderIdentity: e.Provider + "\x00" + e.Model + "\x00" + e.ModelPath,
	}
}

// Handle processes a single request/response cycle for semantic caching.
// It must be called after guardrail patching so params_hash reflects the final policy.
func (m *semanticCacheMiddleware) Handle(c *echo.Context, body []byte, next func() error) error {
	if m == nil || m.store == nil {
		return next()
	}

	path := c.Request().URL.Path
	if !cacheablePaths[path] || c.Request().Method != http.MethodPost {
		return next()
	}

	if shouldSkipSemanticCache(c.Request()) {
		return next()
	}

	if isStreamingRequest(path, body) {
		return next()
	}

	ctx := c.Request().Context()
	plan := core.GetExecutionPlan(ctx)

	embedText, msgCount := extractEmbedText(body, m.cfg.ExcludeSystemPrompt)
	if embedText == "" {
		return next()
	}

	threshold := m.cfg.SimilarityThreshold
	if v := headerFloat64(c.Request(), "X-Cache-Semantic-Threshold"); v > 0 {
		threshold = v
	}

	if m.cfg.MaxConversationMessages > 0 && msgCount > m.cfg.MaxConversationMessages {
		return next()
	}

	msgFp, fpOK := conversationInvariantFingerprint(body, m.cfg.ExcludeSystemPrompt)
	if !fpOK {
		return next()
	}
	baseParams := computeParamsHash(body, path, plan, core.GetGuardrailsHash(ctx), m.embedderIdentity)
	paramsHash := sha256HexOf(baseParams + "\x00" + msgFp)

	vec, err := m.embedder.Embed(ctx, embedText)
	if err != nil {
		slog.Warn("semantic cache: embed failed, bypassing", "err", err)
		return next()
	}

	results, err := m.store.Search(ctx, vec, paramsHash, 1)
	if err != nil {
		slog.Warn("semantic cache: search failed, bypassing", "err", err)
		return next()
	}

	if len(results) > 0 && float64(results[0].Score) >= threshold {
		c.Response().Header().Set("Content-Type", "application/json")
		c.Response().Header().Set("X-Cache", "HIT (semantic)")
		c.Response().WriteHeader(http.StatusOK)
		_, _ = c.Response().Write(results[0].Response)
		slog.Info("semantic cache hit",
			"path", path,
			"score", results[0].Score,
			"request_id", c.Request().Header.Get("X-Request-ID"),
		)
		return nil
	}

	capture := &responseCapture{
		ResponseWriter: c.Response(),
		body:           &bytes.Buffer{},
	}
	c.SetResponse(capture)

	if err := next(); err != nil {
		return err
	}

	if capture.status != http.StatusOK || capture.body.Len() == 0 {
		return nil
	}
	if core.GetFallbackUsed(c.Request().Context()) {
		return nil
	}

	data := bytes.Clone(capture.body.Bytes())
	ttl := time.Duration(m.cfg.TTL) * time.Second
	if ttl == 0 {
		ttl = time.Hour
	}
	if v := headerDuration(c.Request(), "X-Cache-TTL"); v > 0 {
		ttl = v
	}

	cacheKey := sha256HexOf(embedText + "\x00" + paramsHash)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.store.Insert(storeCtx, cacheKey, vec, data, paramsHash, ttl); err != nil {
			slog.Warn("semantic cache: store failed", "key", cacheKey, "err", err)
		}
	}()

	return nil
}

func (m *semanticCacheMiddleware) close() error {
	m.wg.Wait()
	if m.embedder != nil {
		_ = m.embedder.Close() //nolint:errcheck
	}
	if m.store != nil {
		return m.store.Close()
	}
	return nil
}

// extractEmbedText returns the text to embed and the total non-system message count.
// When excludeSystem is true, system messages are stripped from counting and embedding.
// Only the last user message is used as the embedding text to maximize cache hit rate.
func extractEmbedText(body []byte, excludeSystem bool) (text string, nonSystemCount int) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		Input string `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", 0
	}

	if len(req.Messages) == 0 {
		return req.Input, 1
	}

	var lastUserText string
	for _, m := range req.Messages {
		if m.Role == "system" && excludeSystem {
			continue
		}
		nonSystemCount++
		if m.Role == "user" {
			lastUserText = extractTextFromContent(m.Content)
		}
	}
	return lastUserText, nonSystemCount
}

// conversationInvariantFingerprint hashes structural cache context: every message's
// role and raw content except the last user turn, where only non-text parts (e.g.
// image_url) are included so paraphrases of the final user text share a namespace.
// Embeddings-style bodies with only "input" return an empty fingerprint (ok=true)
// because wording is represented solely by the vector. ok is false if the JSON
// envelope cannot be parsed or neither messages nor input is present.
func conversationInvariantFingerprint(body []byte, excludeSystem bool) (fingerprint string, ok bool) {
	var envelope struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", false
	}
	if len(envelope.Messages) == 0 {
		if len(envelope.Input) == 0 {
			return "", false
		}
		return "", true
	}

	type msgPart struct {
		role        string
		content     json.RawMessage
		unparseable bool
		rawMsg      json.RawMessage
	}
	var included []msgPart
	for _, rawMsg := range envelope.Messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(rawMsg, &m); err != nil {
			var roleOnly struct {
				Role string `json:"role"`
			}
			_ = json.Unmarshal(rawMsg, &roleOnly)
			if roleOnly.Role == "system" && excludeSystem {
				continue
			}
			included = append(included, msgPart{role: roleOnly.Role, unparseable: true, rawMsg: rawMsg})
			continue
		}
		if m.Role == "system" && excludeSystem {
			continue
		}
		included = append(included, msgPart{role: m.Role, content: m.Content})
	}

	lastUser := -1
	for i := len(included) - 1; i >= 0; i-- {
		if included[i].role == "user" {
			lastUser = i
			break
		}
	}

	h := sha256.New()
	for i, p := range included {
		h.Write([]byte(p.role))
		h.Write([]byte{0})
		if p.unparseable {
			sum := sha256.Sum256(p.rawMsg)
			h.Write(sum[:])
			h.Write([]byte{0})
			continue
		}
		if i == lastUser && lastUser >= 0 {
			writeNonTextUserContentFingerprint(h, p.content)
		} else if len(p.content) > 0 {
			h.Write(p.content)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func writeNonTextUserContentFingerprint(h hash.Hash, content json.RawMessage) {
	if len(bytes.TrimSpace(content)) == 0 {
		return
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return
	}
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		_, _ = h.Write(content)
		return
	}
	for _, p := range parts {
		var obj map[string]json.RawMessage
		if json.Unmarshal(p, &obj) != nil {
			_, _ = h.Write(p)
			_, _ = h.Write([]byte{0})
			continue
		}
		tBytes, hasType := obj["type"]
		if !hasType {
			_, _ = h.Write(p)
			_, _ = h.Write([]byte{0})
			continue
		}
		var typeStr string
		_ = json.Unmarshal(tBytes, &typeStr)
		if typeStr == "text" {
			continue
		}
		_, _ = h.Write(p)
		_, _ = h.Write([]byte{0})
	}
}

func extractTextFromContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// computeParamsHash builds a stable SHA-256 hash of all output-shaping parameters.
// This ensures semantically similar prompts with different parameters or guardrail
// policies never share a cache entry. endpointPath is the raw URL path
// (e.g. "/v1/chat/completions") and isolates entries across distinct endpoints.
func computeParamsHash(body []byte, endpointPath string, plan *core.ExecutionPlan, guardrailsHash, embedderIdentity string) string {
	var req struct {
		Model          string           `json:"model"`
		Temperature    *float64         `json:"temperature"`
		TopP           *float64         `json:"top_p"`
		MaxTokens      *int             `json:"max_tokens"`
		Tools          []map[string]any `json:"tools"`
		ResponseFormat any              `json:"response_format"`
		Stream         bool             `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)

	h := sha256.New()
	h.Write([]byte(req.Model))
	h.Write([]byte{0})
	h.Write([]byte(endpointPath))
	h.Write([]byte{0})

	if plan != nil {
		h.Write([]byte(plan.ProviderType))
		h.Write([]byte{0})
		h.Write([]byte(plan.ResolvedQualifiedModel()))
		h.Write([]byte{0})
	}

	if req.Temperature != nil {
		h.Write([]byte(strconv.FormatFloat(*req.Temperature, 'f', -1, 64)))
	}
	h.Write([]byte{0})

	if req.TopP != nil {
		h.Write([]byte(strconv.FormatFloat(*req.TopP, 'f', -1, 64)))
	}
	h.Write([]byte{0})

	if req.MaxTokens != nil {
		h.Write([]byte(strconv.Itoa(*req.MaxTokens)))
	}
	h.Write([]byte{0})

	if len(req.Tools) > 0 {
		toolsJSON, _ := json.Marshal(sortedTools(req.Tools))
		xx := xxhash.Sum64(toolsJSON)
		h.Write([]byte(strconv.FormatUint(xx, 16)))
	}
	h.Write([]byte{0})

	if req.ResponseFormat != nil {
		rfJSON, _ := json.Marshal(req.ResponseFormat)
		h.Write(rfJSON)
	}
	h.Write([]byte{0})

	h.Write([]byte(strconv.FormatBool(req.Stream)))
	h.Write([]byte{0})

	h.Write([]byte(guardrailsHash))
	h.Write([]byte{0})
	h.Write([]byte(embedderIdentity))

	return hex.EncodeToString(h.Sum(nil))
}

func sortedTools(tools []map[string]any) []map[string]any {
	sorted := make([]map[string]any, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool {
		ni, _ := sorted[i]["function"].(map[string]any)
		nj, _ := sorted[j]["function"].(map[string]any)
		if ni == nil || nj == nil {
			return false
		}
		namei, _ := ni["name"].(string)
		namej, _ := nj["name"].(string)
		return namei < namej
	})
	return sorted
}

func shouldSkipSemanticCache(req *http.Request) bool {
	if shouldSkipCache(req) {
		return true
	}
	ct := req.Header.Get("X-Cache-Type")
	return strings.EqualFold(ct, "exact")
}

func headerFloat64(req *http.Request, name string) float64 {
	s := req.Header.Get(name)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func headerDuration(req *http.Request, name string) time.Duration {
	s := req.Header.Get(name)
	if s == "" {
		return 0
	}
	seconds, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func sha256HexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ComputeGuardrailsHash computes the guardrails_hash for a set of rule identifiers.
// Each rule is represented as "name:type:order:mode:content_hash". The combined seed is
// sorted for stability, then passed through SHA-256.
// Uses xxhash64 per-component and SHA-256 for the final hash to balance speed and
// collision resistance.
func ComputeGuardrailsHash(rules []GuardrailRuleDescriptor) string {
	if len(rules) == 0 {
		return ""
	}
	seeds := make([]string, len(rules))
	for i, r := range rules {
		contentXX := xxhash.Sum64String(r.Content)
		seeds[i] = fmt.Sprintf("%s:%s:%d:%s:%016x", r.Name, r.Type, r.Order, r.Mode, contentXX)
	}
	sort.Strings(seeds)
	combined := strings.Join(seeds, "|")
	h := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(h[:])
}

// GuardrailRuleDescriptor describes a single active guardrail rule for hashing.
type GuardrailRuleDescriptor struct {
	Name    string
	Type    string
	Order   int
	Mode    string
	Content string
}

// GuardrailsHashFromContext retrieves the guardrails hash from the context,
// using the core package's storage key.
func GuardrailsHashFromContext(ctx context.Context) string {
	return core.GetGuardrailsHash(ctx)
}

// WithGuardrailsHash stores the guardrails hash into the context.
func WithGuardrailsHash(ctx context.Context, hash string) context.Context {
	return core.WithGuardrailsHash(ctx, hash)
}

// CacheTypeHeader values for X-Cache-Type.
const (
	CacheTypeExact    = "exact"
	CacheTypeSemantic = "semantic"
	CacheTypeBoth     = "both"
)

// ShouldSkipExactCache reports whether the X-Cache-Type header requests semantic-only mode.
func ShouldSkipExactCache(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("X-Cache-Type"), CacheTypeSemantic)
}

// ShouldSkipAllCache reports whether caching must be bypassed for this request
// (X-Cache-Control: no-store or Cache-Control containing no-store), matching
// shouldSkipSemanticCache / shouldSkipCache semantics for the no-store directive.
func ShouldSkipAllCache(req *http.Request) bool {
	if strings.EqualFold(req.Header.Get("X-Cache-Control"), "no-store") {
		return true
	}
	cc := req.Header.Get("Cache-Control")
	if cc == "" {
		return false
	}
	directives := strings.Split(strings.ToLower(cc), ",")
	for _, d := range directives {
		d = strings.TrimSpace(d)
		if d == "no-store" {
			return true
		}
	}
	return false
}

// IoReadAllBody reads and restores c.Request().Body, returning the raw bytes.
// Safe to call multiple times — each call resets the body.
func IoReadAllBody(c *echo.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
