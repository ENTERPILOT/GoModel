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

	"github.com/google/uuid"

	"gomodel/config"
)

type weaviateStore struct {
	baseURL    string
	class      string
	apiKey     string
	httpClient *http.Client
	cleanup    *vecCleanup
}

func newWeaviateStore(cfg config.WeaviateConfig) (*weaviateStore, error) {
	base := trimSlash(cfg.URL)
	if base == "" {
		return nil, fmt.Errorf("vecstore weaviate: url is required")
	}
	class := strings.TrimSpace(cfg.Class)
	if class == "" {
		return nil, fmt.Errorf("vecstore weaviate: class is required")
	}
	if err := validateWeaviateClass(class); err != nil {
		return nil, err
	}
	s := &weaviateStore{
		baseURL:    base,
		class:      class,
		apiKey:     strings.TrimSpace(cfg.APIKey),
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.ensureClass(ctx); err != nil {
		return nil, err
	}
	s.cleanup = startVecCleanup(s)
	return s, nil
}

func validateWeaviateClass(name string) error {
	for i, r := range name {
		if i == 0 {
			if r < 'A' || r > 'Z' {
				return fmt.Errorf("vecstore weaviate: class must start with a capital letter (GraphQL), got %q", name)
			}
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return fmt.Errorf("vecstore weaviate: invalid class name %q", name)
	}
	return nil
}

func (s *weaviateStore) Close() error {
	s.cleanup.close()
	return nil
}

func (s *weaviateStore) authHeader(req *http.Request) {
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
}

func (s *weaviateStore) ensureClass(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/v1/schema/"+s.class, nil)
	if err != nil {
		return err
	}
	s.authHeader(req)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	schema := map[string]any{
		"class":      s.class,
		"vectorizer": "none",
		"properties": []any{
			map[string]any{"name": "cache_key", "dataType": []string{"text"}},
			map[string]any{"name": "params_hash", "dataType": []string{"text"}},
			map[string]any{"name": "expires_at", "dataType": []string{"number"}},
			map[string]any{"name": "response_b64", "dataType": []string{"text"}},
		},
	}
	b, _ := json.Marshal(schema)
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/schema", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req2.Header.Set("Content-Type", "application/json")
	s.authHeader(req2)
	resp2, err := s.httpClient.Do(req2)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	raw, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		return fmt.Errorf("vecstore weaviate: create class: %s: %s", resp2.Status, string(raw))
	}
	return nil
}

func (s *weaviateStore) objectID(key, paramsHash string) string {
	ns := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	return uuid.NewSHA1(ns, []byte(key+"\x00"+paramsHash)).String()
}

func (s *weaviateStore) Insert(ctx context.Context, key string, vec []float32, response []byte, paramsHash string, ttl time.Duration) error {
	var expiresAt float64
	if ttl > 0 {
		expiresAt = float64(time.Now().Add(ttl).Unix())
	}
	id := s.objectID(key, paramsHash)
	body := map[string]any{
		"class": s.class,
		"id":    id,
		"properties": map[string]any{
			"cache_key":    key,
			"params_hash":  paramsHash,
			"expires_at":   expiresAt,
			"response_b64": base64.StdEncoding.EncodeToString(response),
		},
		"vector": vec,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := s.baseURL + "/v1/objects/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	s.authHeader(req)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vecstore weaviate: upsert object: %s: %s", resp.Status, string(raw))
	}
	return nil
}

func (s *weaviateStore) Search(ctx context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error) {
	query := fmt.Sprintf(`
{
  Get {
    %s(
      nearVector: { vector: %s }
      where: {
        path: ["params_hash"]
        operator: Equal
        valueText: %q
      }
      limit: %d
    ) {
      cache_key
      response_b64
      expires_at
      _additional {
        distance
        certainty
      }
    }
  }
}`, s.class, floatSliceJSON(vec), paramsHash, limit)
	return s.runGraphQLSearch(ctx, query)
}

func floatSliceJSON(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf("%g", x))
	}
	b.WriteByte(']')
	return b.String()
}

func (s *weaviateStore) runGraphQLSearch(ctx context.Context, query string) ([]VecResult, error) {
	body := map[string]any{"query": query}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/graphql", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.authHeader(req)
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
		return nil, fmt.Errorf("vecstore weaviate: graphql: %s: %s", resp.Status, string(raw))
	}
	var envelope struct {
		Data   map[string]map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("vecstore weaviate: decode graphql: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("vecstore weaviate: graphql error: %s", envelope.Errors[0].Message)
	}
	get := envelope.Data["Get"]
	if get == nil {
		return nil, nil
	}
	itemsRaw, ok := get[s.class]
	if !ok || len(itemsRaw) == 0 {
		return nil, nil
	}
	var items []struct {
		CacheKey    string `json:"cache_key"`
		ResponseB64 string `json:"response_b64"`
		ExpiresAt   float64 `json:"expires_at"`
		Additional  struct {
			Distance   *float64 `json:"distance"`
			Certainty  *float64 `json:"certainty"`
		} `json:"_additional"`
	}
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, fmt.Errorf("vecstore weaviate: decode hits: %w", err)
	}
	now := float64(time.Now().Unix())
	out := make([]VecResult, 0, len(items))
	for _, it := range items {
		if it.ExpiresAt > 0 && it.ExpiresAt < now {
			continue
		}
		rb, err := base64.StdEncoding.DecodeString(it.ResponseB64)
		if err != nil {
			continue
		}
		score := float32(0)
		if it.Additional.Certainty != nil {
			score = float32(*it.Additional.Certainty)
		} else if it.Additional.Distance != nil {
			d := float32(*it.Additional.Distance)
			score = 1 - d
			if score < 0 {
				score = 0
			}
			if score > 1 {
				score = 1
			}
		}
		out = append(out, VecResult{Key: it.CacheKey, Score: score, Response: rb})
	}
	return out, nil
}

func (s *weaviateStore) DeleteExpired(ctx context.Context) error {
	now := time.Now().Unix()
	mutation := fmt.Sprintf(`
mutation {
  Delete {
    %s(
      where: {
        operator: And
        operands: [
          { path: ["expires_at"], operator: GreaterThan, valueNumber: 0 },
          { path: ["expires_at"], operator: LessThan, valueNumber: %d }
        ]
      }
    ) {
      successful
    }
  }
}`, s.class, now)
	body := map[string]any{"query": mutation}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/graphql", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	s.authHeader(req)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vecstore weaviate: delete expired: %s: %s", resp.Status, string(raw))
	}
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("vecstore weaviate: delete expired: %s", envelope.Errors[0].Message)
	}
	return nil
}
