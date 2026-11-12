package storage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type txContextKey struct{}

// fakeTransactor runs the callback once with a marked context, like a session
// that never retries, then returns sessionErr if the callback succeeded.
type fakeTransactor struct {
	sessionErr  error
	callbackErr error
}

func (f *fakeTransactor) WithTransaction(ctx context.Context, fn func(context.Context) (any, error), _ ...options.Lister[options.TransactionOptions]) (any, error) {
	if _, err := fn(context.WithValue(ctx, txContextKey{}, true)); err != nil {
		f.callbackErr = err
		return nil, err
	}
	return nil, f.sessionErr
}

const standaloneMessage = "Transaction numbers are only allowed on a replica set member or mongos"

func TestMongoTransaction(t *testing.T) {
	standalone := mongo.CommandError{Code: 20, Message: standaloneMessage, Labels: []string{"TransientTransactionError"}}
	standaloneText := errors.New(standaloneMessage)
	writeErr := errors.New("write failed")

	tests := []struct {
		name string
		// sessionErr is what the session reports after the callback succeeds.
		sessionErr error
		// results are the callback's errors per call.
		results      []error
		wantTxCalls  []bool
		wantErr      string
		wantErrIs    []error
		wantFallback bool
	}{
		{
			name:        "transaction commits",
			results:     []error{nil},
			wantTxCalls: []bool{true},
		},
		{
			name:        "transaction fails",
			results:     []error{writeErr},
			wantTxCalls: []bool{true},
			wantErr:     "save things transaction: write failed",
			wantErrIs:   []error{writeErr},
		},
		{
			name:         "session cannot start a transaction",
			sessionErr:   standalone,
			results:      []error{nil, nil},
			wantTxCalls:  []bool{true, false},
			wantFallback: true,
		},
		{
			name:         "callback reports the server cannot run transactions",
			results:      []error{standalone, nil},
			wantTxCalls:  []bool{true, false},
			wantFallback: true,
		},
		{
			name:         "fallback fails",
			results:      []error{standaloneText, writeErr},
			wantTxCalls:  []bool{true, false},
			wantErr:      "save things without transaction: ",
			wantErrIs:    []error{standaloneText, writeErr},
			wantFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := slog.Default()
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })

			session := &fakeTransactor{sessionErr: tt.sessionErr}
			var txCalls []bool
			err := MongoTransaction(context.Background(), session, "save things", func(ctx context.Context) error {
				require.Less(t, len(txCalls), len(tt.results), "unexpected callback call")
				txCalls = append(txCalls, ctx.Value(txContextKey{}) != nil)
				return tt.results[len(txCalls)-1]
			})

			assert.Equal(t, tt.wantTxCalls, txCalls)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
			for _, target := range tt.wantErrIs {
				require.ErrorIs(t, err, target)
			}
			if tt.wantFallback {
				assert.Contains(t, logs.String(), "falling back to non-transactional update")
				assert.Contains(t, logs.String(), "operation=\"save things\"")
			} else {
				assert.Empty(t, logs.String())
			}
		})
	}
}

// WithTransaction retries a callback whose error carries the
// TransientTransactionError label; a server that cannot run transactions
// would make it retry until its two-minute deadline.
func TestMongoTransactionHidesCapabilityErrorLabels(t *testing.T) {
	session := &fakeTransactor{}
	calls := 0
	err := MongoTransaction(context.Background(), session, "save things", func(context.Context) error {
		calls++
		if calls == 1 {
			return mongo.CommandError{Message: "transaction is not supported by this deployment", Labels: []string{"TransientTransactionError"}}
		}
		return nil
	})
	require.NoError(t, err)
	require.Error(t, session.callbackErr)
	var labeled mongo.LabeledError
	assert.NotErrorAs(t, session.callbackErr, &labeled, "the session must not see a retryable label")
}

func TestIsMongoTransactionCapabilityError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "standalone transaction message",
			err:  errors.New(standaloneMessage),
			want: true,
		},
		{
			name: "illegal operation command code",
			err: mongo.CommandError{
				Code:    20,
				Message: "transaction is not supported by this deployment",
				Labels:  []string{"TransientTransactionError"},
			},
			want: true,
		},
		{
			name: "labeled unsupported transaction message",
			err: mongo.CommandError{
				Message: "transaction is not supported by this deployment",
				Labels:  []string{"TransientTransactionError"},
			},
			want: true,
		},
		{
			name: "ordinary transient transaction error",
			err: mongo.CommandError{
				Message: "temporary write conflict",
				Labels:  []string{"TransientTransactionError"},
			},
			want: false,
		},
		{
			name: "ordinary error",
			err:  errors.New("network timeout"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isMongoTransactionCapabilityError(tt.err))
		})
	}
}
