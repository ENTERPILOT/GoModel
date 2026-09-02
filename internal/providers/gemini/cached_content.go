package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

type geminiCacheObject struct {
	name       string
	expiresAt  time.Time
	retryAfter time.Time
}

type geminiCreateCachedContentRequest struct {
	Model             string          `json:"model"`
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	TTL               string          `json:"ttl"`
}

type geminiCreateCachedContentResponse struct {
	Name       string    `json:"name"`
	ExpireTime time.Time `json:"expireTime"`
}

// prepareCachedContent materializes the stable prefix selected by the
// post-routing planner. Cache creation is best-effort: an unsupported model or
// endpoint must never turn a request that would otherwise work into a failure.
func (p *Provider) prepareCachedContent(ctx context.Context, req *core.ChatRequest, body *geminiGenerateContentRequest) {
	if body == nil || body.CachedContent != "" || p.backend != geminiBackendAIStudio || len(body.Contents) == 0 {
		return
	}
	if req.PromptCachePlan == nil || strings.TrimSpace(req.PromptCachePlan.Key) == "" {
		return
	}
	// The final content is the live turn. A system instruction, tools, or an
	// earlier content item must remain before it for a useful cached prefix.
	if len(body.Contents) == 1 && body.SystemInstruction == nil && len(body.Tools) == 0 {
		return
	}
	key, ok := p.scopedCachedContentKey(ctx, req.PromptCachePlan.Key)
	if !ok {
		return
	}
	if cached, suppress := p.cachedContentObject(key, time.Now()); suppress {
		if cached != "" {
			useGeminiCachedPrefix(body, cached)
		}
		return
	}

	value, _, _ := p.cacheFlight.Do(key, func() (any, error) {
		now := time.Now()
		if cached, suppress := p.cachedContentObject(key, now); suppress {
			return cached, nil
		}
		// Cache creation may outlive the request that happened to lead the
		// singleflight. Keep affinity values while detaching cancellation.
		createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), geminiCacheCreateTimeout)
		defer cancel()
		createReq := geminiCreateCachedContentRequest{
			Model:             "models/" + normalizeGeminiModelID(req.Model),
			SystemInstruction: body.SystemInstruction,
			Contents:          append([]geminiContent(nil), body.Contents[:len(body.Contents)-1]...),
			Tools:             append([]geminiTool(nil), body.Tools...),
			TTL:               fmt.Sprintf("%ds", geminiCacheTTL/time.Second),
		}
		var created geminiCreateCachedContentResponse
		err := p.nativeClient.Do(createCtx, llmclient.Request{
			Method: http.MethodPost, Endpoint: "/cachedContents", Body: &createReq,
		}, &created)
		now = time.Now()
		if err != nil || strings.TrimSpace(created.Name) == "" {
			if err == nil {
				err = fmt.Errorf("cached-content creation returned an empty name")
			}
			slog.DebugContext(ctx, "Gemini cached-content creation skipped", "model", req.Model, "error", err)
			p.storeCachedContentObject(key, geminiCacheObject{retryAfter: now.Add(geminiCacheFailureBackoff)}, now)
			return "", nil
		}
		expiresAt := created.ExpireTime
		if expiresAt.IsZero() {
			expiresAt = now.Add(geminiCacheTTL)
		}
		if !now.Add(geminiCacheFreshness).Before(expiresAt) {
			p.storeCachedContentObject(key, geminiCacheObject{retryAfter: now.Add(geminiCacheFailureBackoff)}, now)
			return "", nil
		}
		p.storeCachedContentObject(key, geminiCacheObject{name: created.Name, expiresAt: expiresAt}, now)
		return created.Name, nil
	})
	if cached, ok := value.(string); ok && cached != "" {
		useGeminiCachedPrefix(body, cached)
	}
}

func (p *Provider) scopedCachedContentKey(ctx context.Context, planKey string) (string, bool) {
	credential, stable := p.keys.StableForContext(ctx)
	if !stable {
		return "", false
	}
	digest := sha256.Sum256([]byte(credential))
	return planKey + ":" + hex.EncodeToString(digest[:]), true
}

func (p *Provider) cachedContentObject(key string, now time.Time) (string, bool) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	entry, ok := p.cacheObjects[key]
	if !ok {
		return "", false
	}
	if entry.name == "" {
		if now.Before(entry.retryAfter) {
			return "", true
		}
		delete(p.cacheObjects, key)
		return "", false
	}
	if now.Add(geminiCacheFreshness).After(entry.expiresAt) {
		delete(p.cacheObjects, key)
		return "", false
	}
	return entry.name, true
}

func (p *Provider) storeCachedContentObject(key string, entry geminiCacheObject, now time.Time) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if p.cacheObjects == nil {
		p.cacheObjects = make(map[string]geminiCacheObject)
	}
	for candidate, cached := range p.cacheObjects {
		if (cached.name == "" && !now.Before(cached.retryAfter)) ||
			(cached.name != "" && now.Add(geminiCacheFreshness).After(cached.expiresAt)) {
			delete(p.cacheObjects, candidate)
		}
	}
	if len(p.cacheObjects) >= geminiCacheObjectLimit {
		for candidate := range p.cacheObjects {
			delete(p.cacheObjects, candidate)
			break
		}
	}
	p.cacheObjects[key] = entry
}

func useGeminiCachedPrefix(body *geminiGenerateContentRequest, name string) {
	body.CachedContent = name
	body.Contents = append([]geminiContent(nil), body.Contents[len(body.Contents)-1])
	body.SystemInstruction = nil
	body.Tools = nil
}
