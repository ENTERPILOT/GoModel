package responsecache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const (
	partitionChatPath = "/v1/chat/completions"
	partitionChatBody = `{"model":"gpt-4","messages":[{"role":"user","content":"partition golden"}]}`
)

func contextWithUserPath(userPath string) context.Context {
	return core.WithEffectiveUserPath(context.Background(), userPath)
}

func requestAs(t *testing.T, path string, body []byte, userPath string) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := echotest.Post(t, path, body)
	req := c.Request()
	c.SetRequest(req.WithContext(core.WithEffectiveUserPath(req.Context(), userPath)))
	return c, rec
}

// partitionedMiddleware builds the requested layers around in-memory stores.
func partitionedMiddleware(t *testing.T, exact, semantic, partition bool) (*ResponseCacheMiddleware, *MapVecStore) {
	t.Helper()
	mw := &ResponseCacheMiddleware{}
	vecStore := NewMapVecStore()
	if exact {
		mw.simple = newSimpleCacheMiddleware(cache.NewMapStore(), time.Hour, nil)
	}
	if semantic {
		mw.semantic = newSemanticCacheMiddleware(&mockEmbedder{vector: []float32{1, 0, 0}}, vecStore, config.SemanticCacheConfig{
			SimilarityThreshold:     0.9,
			TTL:                     new(3600),
			MaxConversationMessages: new(10),
		}, nil)
	}
	mw.setPartitionByUserPath(partition)
	t.Cleanup(func() { _ = mw.Close() })
	return mw, vecStore
}

// flushCacheWrites waits for the asynchronous writes of every layer.
func flushCacheWrites(mw *ResponseCacheMiddleware) {
	if mw.simple != nil {
		mw.simple.wg.Wait()
	}
	if mw.semantic != nil {
		mw.semantic.wg.Wait()
	}
}

// partitionServed is what one request through the cache observed.
type partitionServed struct {
	xCache     string
	body       string
	calledNext bool
}

// serveAs drives HandleRequest as one user path. A miss answers with a body
// naming that path, so a replayed answer reveals whose it was.
func serveAs(t *testing.T, mw *ResponseCacheMiddleware, path string, body []byte, stream bool, userPath string) partitionServed {
	t.Helper()
	c, rec := requestAs(t, path, body, userPath)
	calledNext := false
	err := mw.HandleRequest(c, body, func() error {
		calledNext = true
		if stream {
			c.Response().Header().Set("Content-Type", "text/event-stream")
			_, writeErr := c.Response().Write([]byte("data: {\"answer\":\"for " + userPath + "\"}\n\ndata: [DONE]\n\n"))
			return writeErr
		}
		return c.JSON(http.StatusOK, map[string]string{"answer": "for " + userPath})
	})
	require.NoError(t, err)
	flushCacheWrites(mw)
	return partitionServed{xCache: rec.Header().Get("X-Cache"), body: rec.Body.String(), calledNext: calledNext}
}

// TestPartitionOff_KeepsTodaysIdentities pins the literal keys the code produced
// before the setting existed, so a deployment leaving it off keeps every entry.
func TestPartitionOff_KeepsTodaysIdentities(t *testing.T) {
	translated := &core.Workflow{Mode: core.ExecutionModeTranslated, ProviderType: "openai"}
	tests := []struct {
		name      string
		path      string
		body      string
		workflow  *core.Workflow
		chainHash string
		want      string
	}{
		{name: "bare chat request", path: partitionChatPath, body: partitionChatBody, want: "cc84e9a497a6312d19dd5f6621283ce221861561b6c9ba05edb462b04389589b"},
		{name: "guardrail chain", path: partitionChatPath, body: partitionChatBody, chainHash: "chain-a", want: "5b661eb300b13d45b3f4424698f6d5a32b580f412758c01d2c70a71adfa7ca82"},
		{name: "resolved workflow", path: partitionChatPath, body: partitionChatBody, workflow: translated, chainHash: "chain-a", want: "141992a18f333cbc224864df87d76fa17143f6a8413ae27c358009ca887545ef"},
		{name: "embeddings", path: embeddingsPath, body: `{"model":"text-embedding-3-small","input":"partition golden"}`, want: "0a847b031e3081b4ca29fa7b8cd77451bdb1acfbcbc155a1e4bf44aa5deeece0"},
	}
	for _, tt := range tests {
		for _, userPath := range []string{"", "/", "/team/a"} {
			t.Run(tt.name+" as "+userPath, func(t *testing.T) {
				c, _ := requestAs(t, tt.path, []byte(tt.body), userPath)
				req := c.Request()
				ctx := core.WithGuardrailsHash(req.Context(), tt.chainHash)
				if tt.workflow != nil {
					ctx = core.WithWorkflow(ctx, tt.workflow)
				}
				c.SetRequest(req.WithContext(ctx))

				m := &simpleCacheMiddleware{}
				assert.Equal(t, tt.want, m.exactCacheKey(&echoExchange{c: c}, []byte(tt.body)))
			})
		}
	}

	for _, userPath := range []string{"", "/team/a"} {
		t.Run("semantic as "+userPath, func(t *testing.T) {
			mw, vecStore := partitionedMiddleware(t, false, true, false)
			serveAs(t, mw, partitionChatPath, []byte(partitionChatBody), false, userPath)

			require.Len(t, vecStore.entries, 1)
			// Recomputed when the params hash started covering every output-shaping
			// field; it still must not vary with the user path while the setting is off.
			assert.Equal(t, "3a5b7dc3ad2b549beb609572a75b4ed6c46004c15be579c2b67bf614a924fcc6", vecStore.entries[0].paramsHash)
			assert.Equal(t, "efd36f887a065acdc1500ef21d3770d0302b05dacb3ecbcd878ce1115b65fd4e", vecStore.entries[0].key)
		})
	}
}

func TestExactCacheKey_PartitionOn(t *testing.T) {
	snapshotPath := func(userPath string) context.Context {
		snapshot := core.NewRequestSnapshot(http.MethodPost, partitionChatPath, nil, nil, nil, "application/json", nil, false, "", nil, userPath)
		return core.WithRequestSnapshot(context.Background(), snapshot)
	}
	boundOverHeader := core.WithEffectiveUserPath(snapshotPath("/team/b"), "/team/a")
	tests := []struct {
		name     string
		first    context.Context
		second   context.Context
		wantSame bool
	}{
		{name: "same path shares a key", first: contextWithUserPath("/team/a"), second: contextWithUserPath("/team/a"), wantSame: true},
		{name: "different paths differ", first: contextWithUserPath("/team/a"), second: contextWithUserPath("/team/b")},
		{name: "no hierarchy inheritance from a parent", first: contextWithUserPath("/team"), second: contextWithUserPath("/team/a")},
		{name: "no user path shares its own key", first: context.Background(), second: contextWithUserPath(""), wantSame: true},
		{name: "no user path is not the root path", first: context.Background(), second: contextWithUserPath("/")},
		{name: "no user path is not a named path", first: context.Background(), second: contextWithUserPath("/team/a")},
		{name: "path from the snapshot counts", first: snapshotPath("/team/a"), second: contextWithUserPath("/team/a"), wantSame: true},
		{name: "effective path wins over the snapshot", first: boundOverHeader, second: contextWithUserPath("/team/a"), wantSame: true},
	}
	body := []byte(partitionChatBody)
	m := &simpleCacheMiddleware{partitionByUserPath: true}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := m.exactCacheKey(&blockingMissExchange{ctx: tt.first}, body)
			second := m.exactCacheKey(&blockingMissExchange{ctx: tt.second}, body)
			if tt.wantSame {
				assert.Equal(t, first, second)
			} else {
				assert.NotEqual(t, first, second)
			}
		})
	}

	t.Run("no partition reuses an unpartitioned key", func(t *testing.T) {
		off := (&simpleCacheMiddleware{}).exactCacheKey(&blockingMissExchange{ctx: context.Background()}, body)
		for _, userPath := range []string{"", "/", "/team/a"} {
			on := m.exactCacheKey(&blockingMissExchange{ctx: contextWithUserPath(userPath)}, body)
			assert.NotEqual(t, off, on, "user path %q", userPath)
		}
	})
}

// Every row serves one layer the same way: each path answers for itself, keeps
// its own entry, and never sees another path's answer.
func TestHandleRequest_PartitionIsolatesEveryEntryPath(t *testing.T) {
	chatStream := `{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"partition"}]}`
	responses := `{"model":"gpt-4","input":"partition"}`
	responsesStream := `{"model":"gpt-4","stream":true,"input":"partition"}`
	tests := []struct {
		name         string
		exact        bool
		semantic     bool
		path         string
		body         string
		stream       bool
		wantHitLabel string
	}{
		{name: "exact chat", exact: true, path: partitionChatPath, body: partitionChatBody, wantHitLabel: CacheHeaderExact},
		{name: "exact chat stream", exact: true, path: partitionChatPath, body: chatStream, stream: true, wantHitLabel: CacheHeaderExact},
		{name: "exact responses", exact: true, path: "/v1/responses", body: responses, wantHitLabel: CacheHeaderExact},
		{name: "exact responses stream", exact: true, path: "/v1/responses", body: responsesStream, stream: true, wantHitLabel: CacheHeaderExact},
		{name: "exact embeddings", exact: true, path: embeddingsPath, body: `{"model":"m","input":"partition"}`, wantHitLabel: CacheHeaderExact},
		{name: "exact messages", exact: true, path: "/v1/messages", body: partitionChatBody, wantHitLabel: CacheHeaderExact},
		{name: "semantic chat", semantic: true, path: partitionChatPath, body: partitionChatBody, wantHitLabel: CacheHeaderSemantic},
		{name: "semantic chat stream", semantic: true, path: partitionChatPath, body: chatStream, stream: true, wantHitLabel: CacheHeaderSemantic},
		{name: "semantic responses", semantic: true, path: "/v1/responses", body: responses, wantHitLabel: CacheHeaderSemantic},
		{name: "semantic responses stream", semantic: true, path: "/v1/responses", body: responsesStream, stream: true, wantHitLabel: CacheHeaderSemantic},
		{name: "both layers", exact: true, semantic: true, path: partitionChatPath, body: partitionChatBody, wantHitLabel: CacheHeaderExact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw, _ := partitionedMiddleware(t, tt.exact, tt.semantic, true)
			body := []byte(tt.body)
			userPaths := []string{"/team/a", "/team/b", "/team/a/sub", "/", ""}

			for _, userPath := range userPaths {
				served := serveAs(t, mw, tt.path, body, tt.stream, userPath)
				assert.True(t, served.calledNext, "%q must miss", userPath)
				assert.Empty(t, served.xCache, "%q must miss", userPath)
			}
			for _, userPath := range userPaths {
				served := serveAs(t, mw, tt.path, body, tt.stream, userPath)
				assert.False(t, served.calledNext, "%q must hit", userPath)
				assert.Equal(t, tt.wantHitLabel, served.xCache)
				assert.Contains(t, served.body, `for `+userPath+`"`, "%q must replay its own answer", userPath)
			}
		})
	}
}

func TestSemanticCache_PartitionConstrainsTheVectorQuery(t *testing.T) {
	tests := []struct {
		name          string
		partition     bool
		first         string
		second        string
		wantHit       bool
		wantSameHash  bool
		wantEntries   int
		secondMessage string
	}{
		{name: "off shares across paths", first: "/team/a", second: "/team/b", wantHit: true, wantSameHash: true, wantEntries: 1},
		{name: "on separates paths", partition: true, first: "/team/a", second: "/team/b", wantEntries: 2},
		{name: "on separates the empty path", partition: true, first: "/team/a", second: "", wantEntries: 2},
		{name: "on keeps a same-path exact repeat", partition: true, first: "/team/a", second: "/team/a", wantHit: true, wantSameHash: true, wantEntries: 1},
		{name: "on keeps a same-path paraphrase", partition: true, first: "/team/a", second: "/team/a", wantHit: true, wantSameHash: true, wantEntries: 1, secondMessage: "rephrased"},
		{name: "on keeps the empty path together", partition: true, first: "", second: "", wantHit: true, wantSameHash: true, wantEntries: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw, vecStore := partitionedMiddleware(t, false, true, tt.partition)
			spy := &searchSpyVecStore{MapVecStore: vecStore}
			mw.semantic.store = spy

			// The mock embedder returns one vector, so every search scores 1 and
			// only the params_hash filter can keep the partitions apart.
			first := []byte(partitionChatBody)
			serveAs(t, mw, partitionChatPath, first, false, tt.first)
			second := first
			if tt.secondMessage != "" {
				second = []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"` + tt.secondMessage + `"}]}`)
			}
			served := serveAs(t, mw, partitionChatPath, second, false, tt.second)

			assert.Equal(t, tt.wantHit, !served.calledNext)
			if tt.wantHit {
				assert.Equal(t, CacheHeaderSemantic, served.xCache)
				assert.Contains(t, served.body, `for `+tt.first+`"`)
			} else {
				assert.Empty(t, served.xCache)
			}
			require.Len(t, spy.searched, 2)
			assert.Equal(t, tt.wantSameHash, spy.searched[0] == spy.searched[1])

			require.Len(t, vecStore.entries, tt.wantEntries)
			assert.Equal(t, spy.searched[0], vecStore.entries[0].paramsHash, "a search must use the hash its writes store")
			if tt.wantEntries == 2 {
				assert.Equal(t, spy.searched[1], vecStore.entries[1].paramsHash)
				assert.NotEqual(t, vecStore.entries[0].key, vecStore.entries[1].key, "partitions must not overwrite each other's points")
			}
		})
	}
}

type searchSpyVecStore struct {
	*MapVecStore
	mu       sync.Mutex
	searched []string
}

func (s *searchSpyVecStore) Search(ctx context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error) {
	s.mu.Lock()
	s.searched = append(s.searched, paramsHash)
	s.mu.Unlock()
	return s.MapVecStore.Search(ctx, vec, paramsHash, limit)
}

// The partition relies on each backend filtering by params_hash inside the
// vector query rather than after it. pgvector needs a live database; its WHERE
// clause is the same kind of filter.
func TestVecStoreSearch_FiltersByParamsHashInTheQuery(t *testing.T) {
	const hash = "partition-params-hash"
	vec := []float32{1, 0, 0}
	tests := []struct {
		name         string
		respond      string
		open         func(url string) (VecStore, error)
		assertFilter func(t *testing.T, body []byte)
	}{
		{
			name:    "qdrant",
			respond: `{"result":[]}`,
			open: func(url string) (VecStore, error) {
				return newQdrantStore(config.QdrantConfig{URL: url, Collection: "c"})
			},
			assertFilter: func(t *testing.T, body []byte) {
				var req struct {
					Filter struct {
						Must []struct {
							Key   string `json:"key"`
							Match struct {
								Value string `json:"value"`
							} `json:"match"`
						} `json:"must"`
					} `json:"filter"`
				}
				require.NoError(t, json.Unmarshal(body, &req))
				var matched []string
				for _, cond := range req.Filter.Must {
					if cond.Key == "params_hash" {
						matched = append(matched, cond.Match.Value)
					}
				}
				assert.Equal(t, []string{hash}, matched, "params_hash must be a filter.must match")
			},
		},
		{
			name:    "pinecone",
			respond: `{"matches":[]}`,
			open: func(url string) (VecStore, error) {
				return newPineconeStore(config.PineconeConfig{Host: url, APIKey: "k", Dimension: len(vec)})
			},
			assertFilter: func(t *testing.T, body []byte) {
				var req struct {
					Filter struct {
						And []map[string]json.RawMessage `json:"$and"`
					} `json:"filter"`
				}
				require.NoError(t, json.Unmarshal(body, &req))
				var matched []string
				for _, cond := range req.Filter.And {
					if raw, ok := cond["params_hash"]; ok {
						matched = append(matched, string(raw))
					}
				}
				assert.Equal(t, []string{`{"$eq":"` + hash + `"}`}, matched, "params_hash must be a filter.$and $eq")
			},
		},
		{
			name:    "weaviate",
			respond: `{"data":{"Get":{"C":[]}}}`,
			open: func(url string) (VecStore, error) {
				return newWeaviateStore(config.WeaviateConfig{URL: url, Class: "C"})
			},
			assertFilter: func(t *testing.T, body []byte) {
				var req struct {
					Query string `json:"query"`
				}
				require.NoError(t, json.Unmarshal(body, &req))
				query := strings.Join(strings.Fields(req.Query), " ")
				assert.Contains(t, query,
					`where: { operator: And operands: [ { path: ["params_hash"] operator: Equal valueText: "`+hash+`" }`,
					"params_hash must be an operand of the where And")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, captured := providertest.JSONServer(t, http.StatusOK, tt.respond)
			store, err := tt.open(upstream.URL)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			_, err = store.Search(context.Background(), vec, hash, 1)
			require.NoError(t, err)

			tt.assertFilter(t, captured.Last(t).Body)
		})
	}
}

func TestStoreAfter_PartitionScopesInFlightCoalescing(t *testing.T) {
	tests := []struct {
		name         string
		partition    bool
		leader       string
		follower     string
		wantCoalesce bool
	}{
		{name: "off coalesces across paths", leader: "/team/a", follower: "/team/b", wantCoalesce: true},
		{name: "on runs another path independently", partition: true, leader: "/team/a", follower: "/team/b"},
		{name: "on runs the empty path independently", partition: true, leader: "/team/a", follower: ""},
		{name: "on coalesces the same path", partition: true, leader: "/team/a", follower: "/team/a", wantCoalesce: true},
		{name: "on coalesces the empty path", partition: true, leader: "", follower: "", wantCoalesce: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSimpleCacheMiddleware(cache.NewMapStore(), time.Hour, nil)
			m.partitionByUserPath = tt.partition
			t.Cleanup(func() { _ = m.close() })
			body := []byte(partitionChatBody)

			var calls atomic.Int32
			next := func() error {
				calls.Add(1)
				return nil
			}
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			leaderDone := make(chan error, 1)
			go func() {
				leaderDone <- m.StoreAfter(&blockingMissExchange{
					ctx: contextWithUserPath(tt.leader), started: started, release: release,
				}, body, next)
			}()
			<-started

			joined := make(chan struct{})
			followerDone := make(chan error, 1)
			go func() {
				followerDone <- m.StoreAfter(&blockingMissExchange{
					ctx: contextWithUserPath(tt.follower), joined: joined,
				}, body, next)
			}()

			if tt.wantCoalesce {
				<-joined
				close(release)
				require.NoError(t, <-leaderDone)
				require.NoError(t, <-followerDone)
				assert.Equal(t, int32(1), calls.Load())
				return
			}
			// The leader is still blocked, so the follower can only finish here
			// if it did not join the leader's call.
			select {
			case err := <-followerDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				close(release)
				require.Fail(t, "the follower waited on another partition's leader")
			}
			close(release)
			require.NoError(t, <-leaderDone)
			assert.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestHandleInternalRequest_PartitionIsolatesUserPaths(t *testing.T) {
	mw, _ := partitionedMiddleware(t, true, true, true)
	body := []byte(partitionChatBody)
	serve := func(userPath string) (*InternalHandleResult, bool) {
		t.Helper()
		calledNext := false
		result, err := mw.HandleInternalRequest(contextWithUserPath(userPath), http.MethodPost, partitionChatPath, body,
			func(context.Context) (*InternalResponse, error) {
				calledNext = true
				return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"answer":"for ` + userPath + `"}`)}, nil
			})
		require.NoError(t, err)
		flushCacheWrites(mw)
		return result, calledNext
	}

	for _, userPath := range []string{"/team/a", "/team/b", ""} {
		_, calledNext := serve(userPath)
		assert.True(t, calledNext, "%q must miss", userPath)
	}
	for _, userPath := range []string{"/team/a", "/team/b", ""} {
		result, calledNext := serve(userPath)
		assert.False(t, calledNext, "%q must hit", userPath)
		assert.Equal(t, CacheTypeExact, result.CacheType)
		assert.Contains(t, string(result.Body), `for `+userPath+`"`)
	}
}

// Changing the setting neither serves nor destroys existing entries: they miss
// in the other mode, age out by TTL, and are reachable again after switching back.
func TestChangingPartitionSetting_MissesExistingEntries(t *testing.T) {
	layers := []struct {
		name     string
		exact    bool
		semantic bool
		wantHit  string
	}{
		{name: "exact", exact: true, wantHit: CacheHeaderExact},
		{name: "semantic", semantic: true, wantHit: CacheHeaderSemantic},
	}
	tests := []struct {
		name          string
		partitionedAt bool
		userPath      string
	}{
		{name: "enabling with no user path", userPath: ""},
		{name: "enabling with the root path", userPath: "/"},
		{name: "enabling with a named path", userPath: "/team/a"},
		{name: "disabling with no user path", partitionedAt: true, userPath: ""},
		{name: "disabling with a named path", partitionedAt: true, userPath: "/team/a"},
	}
	body := []byte(partitionChatBody)
	for _, layer := range layers {
		for _, tt := range tests {
			t.Run(layer.name+" "+tt.name, func(t *testing.T) {
				mw, _ := partitionedMiddleware(t, layer.exact, layer.semantic, tt.partitionedAt)
				primed := serveAs(t, mw, partitionChatPath, body, false, tt.userPath)
				require.True(t, primed.calledNext)

				mw.setPartitionByUserPath(!tt.partitionedAt)
				changed := serveAs(t, mw, partitionChatPath, body, false, tt.userPath)
				assert.True(t, changed.calledNext, "an entry from the other mode must miss")
				assert.Empty(t, changed.xCache)

				mw.setPartitionByUserPath(tt.partitionedAt)
				restored := serveAs(t, mw, partitionChatPath, body, false, tt.userPath)
				assert.False(t, restored.calledNext, "the original entry stays until its TTL")
				assert.Equal(t, layer.wantHit, restored.xCache)
			})
		}
	}
}

func TestNewResponseCacheMiddleware_AppliesPartitionSetting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			m, err := NewResponseCacheMiddleware(config.ResponseCacheConfig{
				PartitionByUserPath: enabled,
				Semantic: &config.SemanticCacheConfig{
					Embedder:    config.EmbedderConfig{Provider: "openai"},
					VectorStore: config.VectorStoreConfig{Type: "qdrant", Qdrant: config.QdrantConfig{URL: "http://127.0.0.1:1", Collection: "c"}},
				},
			}, map[string]config.RawProviderConfig{"openai": {BaseURL: "http://127.0.0.1:1/v1", APIKey: "k"}}, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Close() })

			require.NotNil(t, m.semantic)
			assert.Equal(t, enabled, m.semantic.partitionByUserPath)
		})
	}

	t.Run("reaches the exact layer", func(t *testing.T) {
		m := NewResponseCacheMiddlewareWithStoreAndUserPathPartition(cache.NewMapStore(), time.Hour)
		t.Cleanup(func() { _ = m.Close() })
		assert.True(t, m.simple.partitionByUserPath)
	})
}
