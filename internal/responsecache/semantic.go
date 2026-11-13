package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/embedding"
)

// semanticCacheWriteJob carries one vector-store insert handed to a background
// worker. Insert arguments are captured by value so the request goroutine can
// return immediately.
type semanticCacheWriteJob struct {
	cacheKey   string
	vec        []float32
	data       []byte
	paramsHash string
	ttl        time.Duration
}

// semanticLookupTimeout bounds the embed plus vector search that runs before
// every semantic miss. Past it the request skips the cache and goes to the
// provider.
const semanticLookupTimeout = 3 * time.Second

var errSemanticLookupTimeout = errors.New("semantic cache lookup timed out")

// SemanticCacheMiddleware implements the vector-similarity response cache layer.
// It is the second cache layer, consulted only after an exact-match miss.
type semanticCacheMiddleware struct {
	embedder         embedding.Embedder
	store            VecStore
	cfg              config.SemanticCacheConfig
	embedderIdentity string
	hitRecorder      func(exchange, []byte, string)
	lookupTimeout    time.Duration

	partitionByUserPath bool

	// Vector-store inserts run on a bounded worker pool so a burst of cache
	// misses cannot spawn unbounded goroutines against the vector store. wg
	// tracks queued jobs; workers drains them; mu guards closed/close(jobs).
	jobs    chan semanticCacheWriteJob
	wg      sync.WaitGroup
	workers sync.WaitGroup
	mu      sync.RWMutex
	closed  bool
}

func newSemanticCacheMiddleware(emb embedding.Embedder, store VecStore, cfg config.SemanticCacheConfig, hitRecorder func(exchange, []byte, string)) *semanticCacheMiddleware {
	m := &semanticCacheMiddleware{
		embedder:         emb,
		store:            store,
		cfg:              cfg,
		embedderIdentity: emb.Identity(),
		hitRecorder:      hitRecorder,
		lookupTimeout:    semanticLookupTimeout,
		jobs:             make(chan semanticCacheWriteJob, cacheWriteQueueSize),
	}
	m.startWorkers()
	return m
}

func (m *semanticCacheMiddleware) startWorkers() {
	for range cacheWriteWorkerCount {
		m.workers.Go(func() {
			for job := range m.jobs {
				storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := m.store.Insert(storeCtx, job.cacheKey, job.vec, job.data, job.paramsHash, job.ttl)
				cancel()
				if err != nil {
					slog.Warn("semantic cache: store failed", "key", job.cacheKey, "err", err)
				}
				m.wg.Done()
			}
		})
	}
}

// enqueueWrite hands a store insert to the worker pool. It mirrors
// simpleCacheMiddleware.enqueueWrite: the read lock spans Add+send so close()
// cannot race an untracked write, and a full queue rolls back the Add rather
// than blocking the request goroutine.
func (m *semanticCacheMiddleware) enqueueWrite(job semanticCacheWriteJob) {
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	m.wg.Add(1)
	select {
	case m.jobs <- job:
		m.mu.RUnlock()
	default:
		m.wg.Done()
		m.mu.RUnlock()
		slog.Warn("semantic cache write queue full", "key", job.cacheKey)
	}
}

// Handle processes a single request/response cycle for semantic caching.
// It must be called after guardrail patching so params_hash reflects the final policy.
func (m *semanticCacheMiddleware) Handle(ex exchange, body []byte, next func() error) error {
	if m == nil || m.store == nil {
		return next()
	}

	path := ex.Path()
	if !semanticCacheablePath(path) || ex.Method() != http.MethodPost {
		return next()
	}

	if shouldSkipSemanticCacheHeaders(ex.RequestHeader) {
		return next()
	}

	ctx := ex.Context()
	plan := core.GetWorkflow(ctx)

	embedText, msgCount := extractEmbedText(body, m.cfg.ExcludeSystemPrompt)
	if embedText == "" {
		return next()
	}

	// The header may only tighten matching. Entries are shared across callers
	// (across every caller of one user path when partitioning is on), so a looser
	// client threshold would return answers cached for unrelated prompts,
	// including other tenants'.
	threshold := m.cfg.SimilarityThreshold
	if v := headerFloat64(ex.RequestHeader, "X-Cache-Semantic-Threshold"); v > threshold {
		threshold = v
	}

	if m.cfg.MaxConversationMessages != nil && *m.cfg.MaxConversationMessages > 0 && msgCount > *m.cfg.MaxConversationMessages {
		return next()
	}

	msgFp, fpOK := conversationInvariantFingerprint(body, m.cfg.ExcludeSystemPrompt)
	if !fpOK {
		return next()
	}
	baseParams := computeParamsHash(body, path, plan, core.GetGuardrailsHash(ctx), m.embedderIdentity, m.cfg.ExcludeSystemPrompt)
	paramsHash := sha256HexOf(baseParams + "\x00" + msgFp)
	if m.partitionByUserPath {
		// Every VecStore backend filters Search by exact params_hash inside the
		// vector query, so this confines matching to the caller's partition. The
		// cache key below derives from it too, so partitions cannot overwrite
		// each other's points.
		paramsHash = userPathPartitionedHash(paramsHash, core.UserPathFromContext(ctx))
	}

	vec, results, ok := m.lookup(ctx, embedText, paramsHash)
	if !ok {
		return next()
	}

	if len(results) > 0 && float64(results[0].Score) >= threshold {
		replayErr := ex.ReplayHit(body, results[0].Response, CacheTypeSemantic)
		if replayErr == nil || replayCommitted(replayErr) {
			ex.MarkHit(CacheTypeSemantic)
			if m.hitRecorder != nil {
				m.hitRecorder(ex, results[0].Response, CacheTypeSemantic)
			}
			slog.Info("semantic cache hit",
				"path", path,
				"score", results[0].Score,
				"request_id", core.GetRequestID(ex.Context()),
			)
			return replayErr
		}
		slog.Warn("semantic cache replay failed", "path", path, "err", replayErr)
	}

	data, ok, err := ex.Capture("semantic cache: failed to capture cacheable response body", next)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ttlSec := 0
	if m.cfg.TTL != nil {
		ttlSec = *m.cfg.TTL
	}
	ttl := time.Duration(ttlSec) * time.Second
	if v := headerDuration(ex.RequestHeader, "X-Cache-TTL"); v > 0 {
		ttl = v
	}

	cacheKey := sha256HexOf(embedText + "\x00" + paramsHash)

	m.enqueueWrite(semanticCacheWriteJob{
		cacheKey:   cacheKey,
		vec:        vec,
		data:       data,
		paramsHash: paramsHash,
		ttl:        ttl,
	})

	return nil
}

// lookup embeds text and searches the vector store under one shared budget.
// ok is false when either step fails or the budget runs out; the caller then
// bypasses the cache, so nothing is written for this request.
func (m *semanticCacheMiddleware) lookup(ctx context.Context, text, paramsHash string) ([]float32, []VecResult, bool) {
	ctx, cancel := context.WithTimeoutCause(ctx, m.lookupTimeout, errSemanticLookupTimeout)
	defer cancel()
	vec, err := m.embedder.Embed(ctx, text)
	if err != nil {
		logSemanticBypass(ctx, "embed", err)
		return nil, nil, false
	}
	results, err := m.store.Search(ctx, vec, paramsHash, 1)
	if err != nil {
		logSemanticBypass(ctx, "search", err)
		return nil, nil, false
	}
	return vec, results, true
}

// logSemanticBypass logs why a lookup was skipped. Running out of the lookup
// budget is expected when the embedder or vector store is degraded, so it logs
// at debug instead of warning on every request.
func logSemanticBypass(ctx context.Context, step string, err error) {
	if errors.Is(context.Cause(ctx), errSemanticLookupTimeout) {
		slog.Debug("semantic cache: lookup timed out, bypassing", "step", step, "request_id", core.GetRequestID(ctx))
		return
	}
	slog.Warn("semantic cache: "+step+" failed, bypassing", "err", err)
}

func (m *semanticCacheMiddleware) close() error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.jobs)
	}
	m.mu.Unlock()
	m.workers.Wait()
	m.wg.Wait()
	if m.embedder != nil {
		_ = m.embedder.Close() //nolint:errcheck
	}
	if m.store != nil {
		return m.store.Close()
	}
	return nil
}

type embedMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// extractEmbedText returns the text to embed and the total non-system message count.
// When excludeSystem is true, system messages are stripped from counting and embedding.
// Only the last user message is used as the embedding text to maximize cache hit rate.
// Supports chat bodies with "messages" and Responses API bodies with "input" as either a
// string or an array of {role, content} items (OpenAI-style).
func extractEmbedText(body []byte, excludeSystem bool) (text string, nonSystemCount int) {
	var envelope struct {
		Messages []embedMessage  `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", 0
	}
	if len(envelope.Messages) > 0 {
		return embedTextFromMessages(envelope.Messages, excludeSystem)
	}
	if len(envelope.Input) == 0 {
		return "", 0
	}
	var s string
	if json.Unmarshal(envelope.Input, &s) == nil {
		if s == "" {
			return "", 0
		}
		return s, 1
	}
	var inputMsgs []embedMessage
	if json.Unmarshal(envelope.Input, &inputMsgs) != nil || len(inputMsgs) == 0 {
		return "", 0
	}
	return embedTextFromMessages(inputMsgs, excludeSystem)
}

func embedTextFromMessages(messages []embedMessage, excludeSystem bool) (text string, nonSystemCount int) {
	var lastUserText string
	for _, m := range messages {
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
// For Responses API, "input" may be a string (empty fingerprint) or a message array
// (same treatment as "messages"). ok is false if the JSON envelope cannot be parsed or
// there is no usable messages/input payload.
func conversationInvariantFingerprint(body []byte, excludeSystem bool) (fingerprint string, ok bool) {
	var envelope struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", false
	}
	msgs, fpOK := messageRawListFromMessagesOrInput(envelope.Messages, envelope.Input)
	if !fpOK {
		return "", false
	}
	if len(msgs) == 0 {
		return "", true
	}

	type msgPart struct {
		role        string
		content     json.RawMessage
		unparseable bool
		rawMsg      json.RawMessage
	}
	var included []msgPart
	for _, rawMsg := range msgs {
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
	for i, part := range slices.Backward(included) {
		if part.role == "user" {
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

func messageRawListFromMessagesOrInput(messages []json.RawMessage, input json.RawMessage) (msgs []json.RawMessage, ok bool) {
	if len(messages) > 0 {
		return messages, true
	}
	if len(input) == 0 {
		return nil, false
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return nil, true
	}
	var arr []json.RawMessage
	if json.Unmarshal(input, &arr) != nil {
		return nil, false
	}
	if len(arr) == 0 {
		return nil, false
	}
	return arr, true
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

// paramsHashPromptFields are top-level request fields never hashed as plain
// fields: messages and input are the prompt, matched by embedding and by
// conversationInvariantFingerprint, and stream and stream_options are hashed
// in normalized form by writeOutputShapingFields. Every field not listed here
// or in paramsHashCallerFields is hashed, so a field GoModel does not know
// about causes a miss, never a hit generated under a different value.
var paramsHashPromptFields = map[string]struct{}{
	"messages":       {},
	"input":          {},
	"stream":         {},
	"stream_options": {},
}

// paramsHashCallerFields identify the caller or steer provider-side prompt
// caching. They never change what the model generates, so they are left out
// of the hash, except on responsesPath: a Responses object echoes them back,
// and a hit must not hand one caller's values to another.
var paramsHashCallerFields = map[string]struct{}{
	"user":                   {},
	"metadata":               {},
	"safety_identifier":      {},
	"prompt_cache_key":       {},
	"prompt_cache_retention": {},
}

const responsesPath = "/v1/responses"

// computeParamsHash builds a stable SHA-256 hash of all output-shaping parameters.
// This ensures semantically similar prompts with different parameters or guardrail
// policies never share a cache entry. endpointPath is the raw URL path
// (e.g. "/v1/chat/completions") and isolates entries across distinct endpoints.
// excludeSystem leaves out a top-level system prompt (Anthropic Messages), as
// conversationInvariantFingerprint leaves out system messages.
func computeParamsHash(body []byte, endpointPath string, plan *core.Workflow, guardrailsHash, embedderIdentity string, excludeSystem bool) string {
	h := sha256.New()
	h.Write([]byte(endpointPath))
	h.Write([]byte{0})

	if plan != nil {
		h.Write([]byte(plan.ProviderType))
		h.Write([]byte{0})
		h.Write([]byte(plan.ResolvedQualifiedModel()))
		h.Write([]byte{0})
	}

	writeOutputShapingFields(h, body, endpointPath, excludeSystem)

	h.Write([]byte(guardrailsHash))
	h.Write([]byte{0})
	h.Write([]byte(embedderIdentity))

	return hex.EncodeToString(h.Sum(nil))
}

// writeOutputShapingFields hashes every top-level field of body that is not a
// prompt field or, outside responsesPath, a caller field, in sorted key order
// with canonical JSON values, so member order and whitespace do not matter.
// Keys are written JSON-encoded, so a key containing a NUL byte cannot pass
// for the separator. A body that is not a JSON object, or that repeats a member
// name, is hashed verbatim instead: a provider may read a duplicated member
// differently from the value decoding keeps.
func writeOutputShapingFields(h hash.Hash, body []byte, endpointPath string, excludeSystem bool) {
	var fields map[string]json.RawMessage
	if hasDuplicateJSONMemberNames(body) || json.Unmarshal(body, &fields) != nil {
		h.Write(body)
		h.Write([]byte{0})
		return
	}

	// Embedding and the conversation fingerprint read messages when a body has
	// both messages and input, while a provider may read input. Hashing both
	// keeps such a body from sharing entries with one whose other field differs.
	_, hasMessages := fields["messages"]
	_, hasInput := fields["input"]
	bothPrompts := hasMessages && hasInput

	keys := make([]string, 0, len(fields))
	for key := range fields {
		_, prompt := paramsHashPromptFields[key]
		if bothPrompts && (key == "messages" || key == "input") {
			prompt = false
		}
		if prompt {
			continue
		}
		if _, caller := paramsHashCallerFields[key]; caller && endpointPath != responsesPath {
			continue
		}
		if key == "system" && excludeSystem {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		encodedKey, _ := json.Marshal(key)
		h.Write(encodedKey)
		h.Write([]byte{0})
		if key == "tools" {
			h.Write(canonicalToolsJSON(fields[key]))
		} else {
			h.Write(canonicalJSON(fields[key]))
		}
		h.Write([]byte{0})
	}

	var stream struct {
		Stream        bool                `json:"stream"`
		StreamOptions *core.StreamOptions `json:"stream_options"`
	}
	_ = json.Unmarshal(body, &stream)
	if stream.Stream {
		h.Write([]byte("stream"))
		h.Write([]byte{0})
		if streamOptions := normalizeStreamOptionsForCache(stream.StreamOptions); streamOptions != nil {
			soJSON, _ := json.Marshal(streamOptions)
			h.Write(soJSON)
		}
		h.Write([]byte{0})
	}
}

// canonicalJSON re-encodes raw with sorted object keys and no insignificant
// whitespace. Numbers keep their literal text, so large integers such as a
// seed are never rounded into a colliding value.
func canonicalJSON(raw json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// canonicalToolsJSON is canonicalJSON for a tools array, ordered by function
// name so the same tool set in a different order shares entries.
func canonicalToolsJSON(raw json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tools []map[string]any
	if err := dec.Decode(&tools); err != nil {
		return canonicalJSON(raw)
	}
	out, err := json.Marshal(sortedTools(tools))
	if err != nil {
		return canonicalJSON(raw)
	}
	return out
}

// sortedTools orders a list of Chat Completions function tools by name. Any
// other list, one that mixes in another tool shape or repeats a name, keeps its
// order: there is no single order to normalize it to.
func sortedTools(tools []map[string]any) []map[string]any {
	names := make([]string, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for i, tool := range tools {
		function, _ := tool["function"].(map[string]any)
		name, _ := function["name"].(string)
		if _, duplicate := seen[name]; name == "" || duplicate {
			return tools
		}
		seen[name] = struct{}{}
		names[i] = name
	}
	order := make([]int, len(tools))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return names[order[a]] < names[order[b]] })
	sorted := make([]map[string]any, len(tools))
	for i, idx := range order {
		sorted[i] = tools[idx]
	}
	return sorted
}

func shouldSkipSemanticCacheHeaders(header func(string) string) bool {
	if shouldSkipCacheControl(header("Cache-Control")) {
		return true
	}
	return strings.EqualFold(header("X-Cache-Type"), "exact")
}

func headerFloat64(header func(string) string, name string) float64 {
	s := header(name)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func headerDuration(header func(string) string, name string) time.Duration {
	s := header(name)
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

// CacheTypeHeader values for X-Cache-Type.
const (
	CacheTypeExact      = "exact"
	CacheTypeSemantic   = "semantic"
	CacheHeaderExact    = "HIT (exact)"
	CacheHeaderSemantic = "HIT (semantic)"
)

// shouldSkipAllCacheHeaders reports whether caching must be bypassed for this
// request, matching the exact-cache middleware semantics for no-cache and no-store.
func shouldSkipAllCacheHeaders(header func(string) string) bool {
	if strings.EqualFold(header("X-Cache-Control"), "no-store") {
		return true
	}
	return shouldSkipCacheControl(header("Cache-Control"))
}
