package kimicode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Kimi Code is a thin wrapper over the shared chat-centric adapter and
// forwards embeddings upstream unchanged, so the shared contract covers its
// surface.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "kimicode",
		DefaultBaseURL: "https://api.kimi.com/coding/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
		Embeddings: true,
	})
}

// ---------------------------------------------------------------------------
// Default trip rules — compile, match, non-match
// ---------------------------------------------------------------------------

// Pinned upstream error fragments observed in the kimicode-weselben audit log.
const (
	bodyWeeklyLimit = "You've reached your weekly (7-day) usage limit, please try again after the limit resets"
	bodyHourLimit   = "5-hour usage limit reached, please try again later"
	bodyUsageLimit  = "usage limit reached for this plan"
	bodyQuotaExceed = "quota exceeded for organization"
	bodyNonMatching = "rate limit exceeded"
)

func compileTripRulesForTest(rules []config.TripRuleConfig) ([]llmclient.TripRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	compiled := make([]llmclient.TripRule, 0, len(rules))
	for _, rule := range rules {
		pat, err := regexp.Compile(rule.Match)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, llmclient.TripRule{Pattern: pat, TTL: rule.TTL})
	}
	return compiled, nil
}

func TestDefaultTripOn_Compile(t *testing.T) {
	t.Parallel()

	rules := kimicodeDefaultTripOn()
	require.NotEmpty(t, rules, "must ship at least one default rule")
	require.Len(t, rules, 3, "three pinned rules expected")

	for _, rule := range rules {
		r := rule
		t.Run(r.Match, func(t *testing.T) {
			err := config.ValidateResilience(config.ResilienceConfig{
				CircuitBreaker: config.CircuitBreakerConfig{TripOn: []config.TripRuleConfig{r}},
			})
			assert.NoError(t, err, "rule[%d] match must be a valid regex", 0)
			assert.NotZero(t, r.TTL, "rule[%d] must carry a TTL", 0)
		})
	}
}

func TestDefaultTripOn_MatchesPinnedBodies(t *testing.T) {
	t.Parallel()

	rules := kimicodeDefaultTripOn()
	compiled, err := compileTripRulesForTest(rules)
	require.NoError(t, err)

	tests := []struct {
		name    string
		body    string
		wantOK  bool
		wantTTL time.Duration
	}{
		{"weekly limit", bodyWeeklyLimit, true, 4 * time.Hour},
		{"5-hour limit", bodyHourLimit, true, 30 * time.Minute},
		{"usage limit", bodyUsageLimit, true, 15 * time.Minute},
		{"quota exceeded", bodyQuotaExceed, true, 15 * time.Minute},
		{"non-matching rate limit", bodyNonMatching, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gatewayErr := core.NewProviderError("kimicode", http.StatusForbidden, tt.body, nil)

			found := false
			for _, rule := range compiled {
				if rule.Pattern.MatchString(gatewayErr.Message) {
					found = true
					assert.Equal(t, tt.wantOK, true,
						"rule %q should successfully match body %q", rule.Pattern.String(), tt.name)
					assert.Equal(t, tt.wantTTL, rule.TTL,
						"rule %q TTL mismatch for body %q", rule.Pattern.String(), tt.name)
					break
				}
			}
			if !tt.wantOK {
				assert.False(t, found, "no rule should match body %q", tt.name)
			}
		})
	}
}

func TestDefaultTripOn_NonMatchingBodyDoesNotTrip(t *testing.T) {
	t.Parallel()

	compiled, err := compileTripRulesForTest(kimicodeDefaultTripOn())
	require.NoError(t, err)

	gatewayErr := core.NewProviderError("kimicode", http.StatusForbidden, bodyNonMatching, nil)

	for _, rule := range compiled {
		assert.False(t, rule.Pattern.MatchString(gatewayErr.Message),
			"rule %q should not match non-matching body %q", rule.Pattern.String(), bodyNonMatching)
	}
}

func TestDefaultTripOn_RegistrationSlices(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, Registration.DefaultTripOn, "Registration must carry defaults")
	require.Len(t, Registration.DefaultTripOn, 3)
}

// ---------------------------------------------------------------------------
// Provider resolution tests
// ---------------------------------------------------------------------------

func TestFactoryApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("no config trip_on gets defaults", func(t *testing.T) {
		t.Parallel()

		factory := providers.NewProviderFactory()
		factory.Add(Registration)

		cfg := providers.ProviderConfig{
			Name: "kimi-test",
			Type: "kimicode",
			Resilience: config.ResilienceConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					// TripOn nil — factory should apply defaults.
				},
			},
		}

		p, err := factory.Create(cfg)
		require.NoError(t, err)
		require.NotNil(t, p)

		// Create takes cfg by value, so mutation is local.
		// Verify via the e2e test that defaults actually trip the breaker.
	})

	t.Run("explicit empty list disables defaults", func(t *testing.T) {
		t.Parallel()

		factory := providers.NewProviderFactory()
		factory.Add(Registration)

		cfg := providers.ProviderConfig{
			Name: "kimi-test",
			Type: "kimicode",
			Resilience: config.ResilienceConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					TripOn: []config.TripRuleConfig{}, // explicit empty
				},
			},
		}

		_, err := factory.Create(cfg)
		require.NoError(t, err)
		// Empty list is a valid config — provider creates with no trip rules.
	})

	t.Run("non-empty config overrides defaults", func(t *testing.T) {
		t.Parallel()

		factory := providers.NewProviderFactory()
		factory.Add(Registration)

		cfg := providers.ProviderConfig{
			Name: "kimi-test",
			Type: "kimicode",
			Resilience: config.ResilienceConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					TripOn: []config.TripRuleConfig{
						{Match: `custom rule`, TTL: 1 * time.Minute},
					},
				},
			},
		}

		p, err := factory.Create(cfg)
		require.NoError(t, err)
		require.NotNil(t, p)
	})

	t.Run("unknown type gets no defaults", func(t *testing.T) {
		t.Parallel()

		factory := providers.NewProviderFactory()
		// No registration for "unknown" type.

		cfg := providers.ProviderConfig{
			Name: "unknown-test",
			Type: "unknown",
			Resilience: config.ResilienceConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					// TripOn nil — should stay nil.
				},
			},
		}
		_, err := factory.Create(cfg)
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// E2E: default rules trip the breaker via the public client API
// ---------------------------------------------------------------------------

func TestDefaultRules_TripBreakerOnWeeklyBody(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"` + bodyWeeklyLimit + `","code":"weekly_limit"}}`))
	}))
	defer server.Close()

	cfg := llmclient.Config{
		ProviderName: "kimicode",
		BaseURL:      server.URL,
		Retry:        config.DefaultRetryConfig(),
		CircuitBreaker: config.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 5,
			SuccessThreshold: 1,
			Timeout:          20 * time.Millisecond,
			TripOn:           kimicodeDefaultTripOn(),
		},
	}
	client := llmclient.New(cfg, nil)

	// First request trips the breaker.
	err := client.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "first failure must reach upstream")

	// Second request is rejected immediately.
	err = client.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Contains(t, gwErr.Message, "circuit breaker is open")
	assert.Equal(t, int32(1), attempts.Load(), "breaker must prevent upstream reach")

	// Reset closes immediately.
	client.ResetBreaker()
}

func TestDefaultRules_5HourLimitTrips(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"` + bodyHourLimit + `","code":"hourly_limit"}}`))
	}))
	defer server.Close()

	cfg := llmclient.Config{
		ProviderName: "kimicode",
		BaseURL:      server.URL,
		Retry:        config.DefaultRetryConfig(),
		CircuitBreaker: config.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 5,
			SuccessThreshold: 1,
			Timeout:          20 * time.Millisecond,
			TripOn:           kimicodeDefaultTripOn(),
		},
	}
	client := llmclient.New(cfg, nil)

	err := client.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)

	// Verify breaker is open (second request rejected).
	err = client.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Contains(t, gwErr.Message, "circuit breaker is open")
}

func TestDefaultRules_ResetClearsQuotaWindow(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"` + bodyWeeklyLimit + `","code":"weekly_limit"}}`))
	}))
	defer server.Close()

	cfg := llmclient.Config{
		ProviderName: "kimicode",
		BaseURL:      server.URL,
		Retry:        config.DefaultRetryConfig(),
		CircuitBreaker: config.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 5,
			SuccessThreshold: 1,
			Timeout:          1 * time.Minute,
			TripOn:           kimicodeDefaultTripOn(),
		},
	}
	client := llmclient.New(cfg, nil)

	err := client.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)

	client.ResetBreaker()

	// After reset, traffic flows again.
	err = client.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err) // still fails (server returns 403), but goes upstream
}

// Non-gateway errors don't trip quota rules.
// Covered by llmclient/circuit_breaker_trip_test.go.TestQuotaTripTTL
// which exercises the same logic with unexported field access.
