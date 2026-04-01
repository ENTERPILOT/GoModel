package responsecache

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gomodel/config"
)

type qdrantStore struct {
	baseURL    string
	collection string
	apiKey     string
	httpClient *http.Client

	mu         sync.Mutex
	vectorSize int

	cleanup *vecCleanup
}

func newQdrantStore(cfg config.QdrantConfig) (*qdrantStore, error) {
	base := trimSlash(cfg.URL)
	if base == "" {
		return nil, fmt.Errorf("vecstore qdrant: url is required")
	}
	coll := cfg.Collection
	if coll == "" {
		return nil, fmt.Errorf("vecstore qdrant: collection is required")
	}
	s := &qdrantStore{
		baseURL:    base,
		collection: coll,
		apiKey:     cfg.APIKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	s.cleanup = startVecCleanup(s)
	return s, nil
}

func (s *qdrantStore) Close() error {
	s.cleanup.close()
	return nil
}

func (s *qdrantStore) req(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("api-key", s.apiKey)
	}
	return s.httpClient.Do(req)
}

func (s *qdrantStore) ensureCollection(ctx context.Context, dim int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vectorSize == dim {
		return nil
	}
	if s.vectorSize != 0 && s.vectorSize != dim {
		return fmt.Errorf("vecstore qdrant: embedding dimension changed from %d to %d", s.vectorSize, dim)
	}
	resp, err := s.req(ctx, http.MethodGet, "/collections/"+s.collection, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var info struct {
			Result struct {
				Config struct {
					Params struct {
						Vectors json.RawMessage `json:"vectors"`
					} `json:"params"`
				} `json:"config"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &info); err == nil {
			var singleVec struct {
				Size int `json:"size"`
			}
			if json.Unmarshal(info.Result.Config.Params.Vectors, &singleVec) == nil && singleVec.Size > 0 {
				if singleVec.Size != dim {
					return fmt.Errorf("vecstore qdrant: collection %q has vector size %d but embedder produces %d", s.collection, singleVec.Size, dim)
				}
			}
		}
		s.vectorSize = dim
		return nil

	case http.StatusNotFound:
		createBody := map[string]any{
			"vectors": map[string]any{
				"size":     dim,
				"distance": "Cosine",
			},
		}
		resp2, err := s.req(ctx, http.MethodPut, "/collections/"+s.collection, createBody)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		raw2, _ := io.ReadAll(resp2.Body)
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			return fmt.Errorf("vecstore qdrant: create collection: %s: %s", resp2.Status, string(raw2))
		}
		s.vectorSize = dim
		return nil

	default:
		return fmt.Errorf("vecstore qdrant: get collection: %s: %s", resp.Status, string(raw))
	}
}

func (s *qdrantStore) Insert(ctx context.Context, key string, vec []float32, response []byte, paramsHash string, ttl time.Duration) error {
	if err := s.ensureCollection(ctx, len(vec)); err != nil {
		return err
	}
	var expiresAt int64
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl).Unix()
	}
	id := vecPointID64(key, paramsHash)
	payload := map[string]any{
		"cache_key":    key,
		"params_hash":  paramsHash,
		"response_b64": base64.StdEncoding.EncodeToString(response),
		"expires_at":   expiresAt,
	}
	body := map[string]any{
		"points": []any{
			map[string]any{
				"id":      id,
				"vector":  vec,
				"payload": payload,
			},
		},
	}
	resp, err := s.req(ctx, http.MethodPut, "/collections/"+s.collection+"/points?wait=true", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vecstore qdrant: upsert: %s: %s", resp.Status, string(raw))
	}
	return nil
}

type qdrantSearchResp struct {
	Result []struct {
		ID      any            `json:"id"`
		Score   float64        `json:"score"`
		Payload map[string]any `json:"payload"`
	} `json:"result"`
}

func (s *qdrantStore) Search(ctx context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error) {
	if len(vec) == 0 {
		return nil, nil
	}
	now := time.Now().Unix()
	filter := map[string]any{
		"must": []any{
			map[string]any{
				"key":   "params_hash",
				"match": map[string]any{"value": paramsHash},
			},
		},
	}
	body := map[string]any{
		"vector":       vec,
		"limit":        limit,
		"with_payload": true,
		"filter":       filter,
	}
	resp, err := s.req(ctx, http.MethodPost, "/collections/"+s.collection+"/points/search", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vecstore qdrant: search: %s: %s", resp.Status, string(raw))
	}
	var parsed qdrantSearchResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("vecstore qdrant: decode search: %w", err)
	}
	out := make([]VecResult, 0, len(parsed.Result))
	for _, hit := range parsed.Result {
		exp, _ := payloadInt64(hit.Payload["expires_at"])
		if exp > 0 && exp < now {
			continue
		}
		b64, _ := hit.Payload["response_b64"].(string)
		rb, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		k, _ := hit.Payload["cache_key"].(string)
		out = append(out, VecResult{
			Key:      k,
			Score:    float32(hit.Score),
			Response: rb,
		})
	}
	return out, nil
}

func payloadInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func (s *qdrantStore) DeleteExpired(ctx context.Context) error {
	now := time.Now().Unix()
	body := map[string]any{
		"filter": map[string]any{
			"must": []any{
				map[string]any{
					"key": "expires_at",
					"range": map[string]any{
						"gt": 0,
						"lt": now,
					},
				},
			},
		},
	}
	resp, err := s.req(ctx, http.MethodPost, "/collections/"+s.collection+"/points/delete?wait=true", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vecstore qdrant: delete expired: %s: %s", resp.Status, string(raw))
	}
	return nil
}
