package usage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type mongoPricingTestContextKey struct{}

type mockMongoPricingSession struct {
	invokeTransaction    bool
	withTransactionError error
	withTransactionCalls int
	endSessionCalls      int
}

func (s *mockMongoPricingSession) WithTransaction(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	s.withTransactionCalls++
	if s.invokeTransaction {
		nextCtx := context.WithValue(ctx, mongoPricingTestContextKey{}, "transaction")
		result, err := fn(nextCtx)
		if err != nil {
			return result, err
		}
	}
	if s.withTransactionError != nil {
		return nil, s.withTransactionError
	}
	return nil, nil
}

func (s *mockMongoPricingSession) EndSession(context.Context) {
	s.endSessionCalls++
}

func TestMongoDBStoreRecalculatePricingTransactionFlow(t *testing.T) {
	session := &mockMongoPricingSession{invokeTransaction: true}
	recalculateCalls := 0
	store := &MongoDBStore{
		startPricingSession: func() (mongoPricingSession, error) {
			return session, nil
		},
		recalculatePricingDocuments: func(ctx context.Context, filter bson.D, _ PricingResolver) (RecalculatePricingResult, error) {
			recalculateCalls++
			require.Equal(t, "transaction", ctx.Value(mongoPricingTestContextKey{}))
			require.True(t, mongoFilterHasProviderSelector(filter, "primary-openai"), "filter = %#v, want provider/provider_name selector", filter)

			return RecalculatePricingResult{Matched: 1, Recalculated: 1, WithPricing: 1}, nil
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{
		Model: " gpt-4o ", Provider: " primary-openai ",
	}, staticTestPricingResolver{})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	require.Equal(t, int64(1), result.Matched)
	require.Equal(t, int64(1), result.Recalculated)
	require.Equal(t, int64(1), result.WithPricing, "result = %+v, want finalized successful result", result)
	require.Equal(t, 1, session.withTransactionCalls)
	require.Equal(t, 1, session.endSessionCalls)
	require.Equal(t, 1, recalculateCalls)
}

func TestMongoDBStoreRecalculatePricingFallsBackWhenTransactionsUnavailable(t *testing.T) {
	originalLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(originalLogger)

	session := &mockMongoPricingSession{
		withTransactionError: errors.New("transaction numbers are only allowed on a replica set member or mongos"),
	}
	recalculateCalls := 0
	store := &MongoDBStore{
		startPricingSession: func() (mongoPricingSession, error) {
			return session, nil
		},
		recalculatePricingDocuments: func(ctx context.Context, _ bson.D, _ PricingResolver) (RecalculatePricingResult, error) {
			recalculateCalls++
			got := ctx.Value(mongoPricingTestContextKey{})
			require.Nil(t, got)

			return RecalculatePricingResult{Matched: 2, Recalculated: 2, WithPricing: 2}, nil
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{}, staticTestPricingResolver{})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	require.Equal(t, int64(2), result.Matched)
	require.Equal(t, int64(2), result.Recalculated)
	require.Equal(t, int64(2), result.WithPricing, "result = %+v, want finalized fallback result", result)
	require.Equal(t, 1, session.withTransactionCalls)
	require.Equal(t, 1, session.endSessionCalls)
	require.Equal(t, 1, recalculateCalls)
	require.Contains(t, logs.String(), "falling back to non-transactional update")
}

func TestMongoDBStoreRecalculatePricingFallsBackWhenTransactionBodyReportsCapabilityError(t *testing.T) {
	session := &mockMongoPricingSession{invokeTransaction: true}
	recalculateCalls := 0
	store := &MongoDBStore{
		startPricingSession: func() (mongoPricingSession, error) {
			return session, nil
		},
		recalculatePricingDocuments: func(ctx context.Context, _ bson.D, _ PricingResolver) (RecalculatePricingResult, error) {
			recalculateCalls++
			switch recalculateCalls {
			case 1:
				got := ctx.Value(mongoPricingTestContextKey{})
				require.Equal(t, "transaction", got)

				return RecalculatePricingResult{}, errors.New("transaction numbers are only allowed on a replica set member or mongos")
			case 2:
				got := ctx.Value(mongoPricingTestContextKey{})
				require.Nil(t, got)

				return RecalculatePricingResult{Matched: 1, Recalculated: 1, WithPricing: 1}, nil
			default:
				t.Fatalf("unexpected recalculate call %d", recalculateCalls)
				return RecalculatePricingResult{}, nil
			}
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{}, staticTestPricingResolver{})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	require.Equal(t, int64(1), result.Matched)
	require.Equal(t, int64(1), result.Recalculated)
	require.Equal(t, int64(1), result.WithPricing, "result = %+v, want finalized fallback result", result)
	require.Equal(t, 2, recalculateCalls)
}

func TestMongoDBStoreRecalculatePricingUsesProviderNameForPricing(t *testing.T) {
	inputRate := 2.0
	resolver := &recordingPricingResolver{
		pricing: &core.ModelPricing{InputPerMtok: &inputRate},
	}
	session := &mockMongoPricingSession{invokeTransaction: true}
	var capturedFilter bson.D
	store := &MongoDBStore{
		startPricingSession: func() (mongoPricingSession, error) {
			return session, nil
		},
		recalculatePricingDocuments: func(_ context.Context, filter bson.D, resolver PricingResolver) (RecalculatePricingResult, error) {
			capturedFilter = filter
			update := recalculateEntryCosts(recalculationEntry{
				ID:           "usage-1",
				Model:        "gpt-4o",
				Provider:     "openai",
				ProviderName: "primary-openai",
				InputTokens:  1_000_000,
			}, resolver)
			result := RecalculatePricingResult{}
			updateRecalculatePricingResult(&result, update)
			return result, nil
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{
		Model: "gpt-4o", Provider: " primary-openai ",
	}, resolver)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.WithPricing, "result = %+v, want pricing match", result)
	require.Equal(t, "primary-openai", resolver.provider)
	require.True(t, mongoFilterHasProviderSelector(capturedFilter, "primary-openai"), "filter = %#v, want provider/provider_name selector", capturedFilter)
}

func mongoFilterHasProviderSelector(filter bson.D, selector string) bool {
	var hasProvider, hasProviderName bool
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case bson.D:
			for _, elem := range typed {
				if elem.Key == "provider" && elem.Value == selector {
					hasProvider = true
				}
				if elem.Key == "provider_name" && elem.Value == selector {
					hasProviderName = true
				}
				visit(elem.Value)
			}
		case bson.A:
			for _, item := range typed {
				visit(item)
			}
		}
	}
	visit(filter)
	return hasProvider && hasProviderName
}
