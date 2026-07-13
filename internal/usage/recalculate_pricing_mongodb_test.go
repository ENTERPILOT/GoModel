package usage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/enterpilot/gomodel/internal/core"
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
			if got := ctx.Value(mongoPricingTestContextKey{}); got != "transaction" {
				t.Fatalf("transaction context marker = %v, want transaction", got)
			}
			if !mongoFilterHasProviderSelector(filter, "primary-openai") {
				t.Fatalf("filter = %#v, want provider/provider_name selector", filter)
			}
			return RecalculatePricingResult{Matched: 1, Recalculated: 1, WithPricing: 1}, nil
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{
		UsageQueryParams: UsageQueryParams{Model: " gpt-4o ", Provider: " primary-openai "},
	}, staticTestPricingResolver{})
	if err != nil {
		t.Fatalf("RecalculatePricing() error = %v", err)
	}
	if result.Status != "ok" || result.Matched != 1 || result.Recalculated != 1 || result.WithPricing != 1 {
		t.Fatalf("result = %+v, want finalized successful result", result)
	}
	if session.withTransactionCalls != 1 || session.endSessionCalls != 1 {
		t.Fatalf("session calls = transaction %d end %d, want 1/1", session.withTransactionCalls, session.endSessionCalls)
	}
	if recalculateCalls != 1 {
		t.Fatalf("recalculate calls = %d, want 1", recalculateCalls)
	}
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
			if got := ctx.Value(mongoPricingTestContextKey{}); got != nil {
				t.Fatalf("fallback context marker = %v, want nil", got)
			}
			return RecalculatePricingResult{Matched: 2, Recalculated: 2, WithPricing: 2}, nil
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{}, staticTestPricingResolver{})
	if err != nil {
		t.Fatalf("RecalculatePricing() error = %v", err)
	}
	if result.Status != "ok" || result.Matched != 2 || result.Recalculated != 2 || result.WithPricing != 2 {
		t.Fatalf("result = %+v, want finalized fallback result", result)
	}
	if session.withTransactionCalls != 1 || session.endSessionCalls != 1 {
		t.Fatalf("session calls = transaction %d end %d, want 1/1", session.withTransactionCalls, session.endSessionCalls)
	}
	if recalculateCalls != 1 {
		t.Fatalf("recalculate calls = %d, want 1", recalculateCalls)
	}
	if !strings.Contains(logs.String(), "falling back to non-transactional update") {
		t.Fatalf("logs = %q, want fallback warning", logs.String())
	}
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
				if got := ctx.Value(mongoPricingTestContextKey{}); got != "transaction" {
					t.Fatalf("transaction context marker = %v, want transaction", got)
				}
				return RecalculatePricingResult{}, errors.New("transaction numbers are only allowed on a replica set member or mongos")
			case 2:
				if got := ctx.Value(mongoPricingTestContextKey{}); got != nil {
					t.Fatalf("fallback context marker = %v, want nil", got)
				}
				return RecalculatePricingResult{Matched: 1, Recalculated: 1, WithPricing: 1}, nil
			default:
				t.Fatalf("unexpected recalculate call %d", recalculateCalls)
				return RecalculatePricingResult{}, nil
			}
		},
	}

	result, err := store.RecalculatePricing(context.Background(), RecalculatePricingParams{}, staticTestPricingResolver{})
	if err != nil {
		t.Fatalf("RecalculatePricing() error = %v", err)
	}
	if result.Status != "ok" || result.Matched != 1 || result.Recalculated != 1 || result.WithPricing != 1 {
		t.Fatalf("result = %+v, want finalized fallback result", result)
	}
	if recalculateCalls != 2 {
		t.Fatalf("recalculate calls = %d, want 2", recalculateCalls)
	}
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
		UsageQueryParams: UsageQueryParams{Model: "gpt-4o", Provider: " primary-openai "},
	}, resolver)
	if err != nil {
		t.Fatalf("RecalculatePricing() error = %v", err)
	}
	if result.WithPricing != 1 {
		t.Fatalf("result = %+v, want pricing match", result)
	}
	if resolver.provider != "primary-openai" {
		t.Fatalf("ResolvePricing provider = %q, want primary-openai", resolver.provider)
	}
	if !mongoFilterHasProviderSelector(capturedFilter, "primary-openai") {
		t.Fatalf("filter = %#v, want provider/provider_name selector", capturedFilter)
	}
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
