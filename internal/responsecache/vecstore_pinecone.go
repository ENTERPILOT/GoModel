package responsecache

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gomodel/config"
)

// pineconeMetadataValueMax is a conservative limit for a single metadata string (Pinecone ~40KB UTF-8).
const pineconeMetadataValueMax = 38000

type pineconeStore struct {
	host       string
	apiKey     string
	namespace  string
	dimension  int
	httpClient *http.Client
	cleanup    *vecCleanup
}

func newPineconeStore(cfg config.PineconeConfig) (*pineconeStore, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, fmt.Errorf("vecstore pinecone: host is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("vecstore pinecone: api_key is required")
	}
	if cfg.Dimension <= 0 {
		return nil, fmt.Errorf("vecstore pinecone: dimension must be > 0")
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	host = trimSlash(host)
	s := &pineconeStore{
		host:       host,
		apiKey:     cfg.APIKey,
		namespace:  cfg.Namespace,
		dimension:  cfg.Dimension,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	s.cleanup = startVecCleanup(s)
	return s, nil
}

func (s *pineconeStore) Close() error {
	s.cleanup.close()
	return nil
}

func (s *pineconeStore) pineconeID(key, paramsHash string) string {
	return fmt.Sprintf("%x", vecPointID64(key, paramsHash))
}

func (s *pineconeStore) Insert(ctx context.Context, key string, vec []float32, response []byte, paramsHash string, ttl time.Duration) error {
	if len(vec) != s.dimension {
		return fmt.Errorf("vecstore pinecone: embedding len %d != configured dimension %d", len(vec), s.dimension)
	}
	b64 := base64.StdEncoding.EncodeToString(response)
	if len(b64) > pineconeMetadataValueMax {
		return fmt.Errorf("vecstore pinecone: cached response too large for Pinecone metadata (~40KB limit after base64); got %d bytes", len(b64))
	}
	var expiresAt int64
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl).Unix()
	}
	body := map[string]any{
		"vectors": []any{
			map[string]any{
				"id":     s.pineconeID(key, paramsHash),
				"values": vec,
				"metadata": map[string]any{
					"cache_key":    key,
					"params_hash":  paramsHash,
					"response_b64": b64,
					"expires_at":   expiresAt,
				},
			},
		},
	}
	if s.namespace != "" {
		body["namespace"] = s.namespace
	}
	return s.postJSON(ctx, "/vectors/upsert", body)
}

func (s *pineconeStore) postJSON(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.host+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", s.apiKey)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vecstore pinecone: %s %s: %s", path, resp.Status, string(raw))
	}
	return nil
}

type pineconeQueryResp struct {
	Matches []struct {
		ID       string         `json:"id"`
		Score    float32        `json:"score"`
		Metadata map[string]any `json:"metadata"`
	} `json:"matches"`
}

func (s *pineconeStore) Search(ctx context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error) {
	if len(vec) != s.dimension {
		return nil, fmt.Errorf("vecstore pinecone: embedding len %d != dimension %d", len(vec), s.dimension)
	}
	now := time.Now().Unix()
	body := map[string]any{
		"vector":          vec,
		"topK":            limit,
		"includeMetadata": true,
		"filter": map[string]any{
			"$and": []any{
				map[string]any{"params_hash": map[string]any{"$eq": paramsHash}},
				map[string]any{
					"$or": []any{
						map[string]any{"expires_at": map[string]any{"$eq": 0}},
						map[string]any{"expires_at": map[string]any{"$gte": now}},
					},
				},
			},
		},
	}
	if s.namespace != "" {
		body["namespace"] = s.namespace
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.host+"/query", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", s.apiKey)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vecstore pinecone: query %s: %s", resp.Status, string(raw))
	}
	var parsed pineconeQueryResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("vecstore pinecone: decode query: %w", err)
	}
	out := make([]VecResult, 0, len(parsed.Matches))
	for _, m := range parsed.Matches {
		b64, _ := m.Metadata["response_b64"].(string)
		rb, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		k, _ := m.Metadata["cache_key"].(string)
		out = append(out, VecResult{Key: k, Score: m.Score, Response: rb})
	}
	return out, nil
}

func (s *pineconeStore) DeleteExpired(ctx context.Context) error {
	now := time.Now().Unix()
	body := map[string]any{
		"filter": map[string]any{
			"$and": []any{
				map[string]any{"expires_at": map[string]any{"$gt": 0}},
				map[string]any{"expires_at": map[string]any{"$lt": now}},
			},
		},
	}
	if s.namespace != "" {
		body["namespace"] = s.namespace
	}
	return s.postJSON(ctx, "/vectors/delete", body)
}
