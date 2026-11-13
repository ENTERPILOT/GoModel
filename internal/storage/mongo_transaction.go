package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoTransactor runs a callback inside a MongoDB transaction. *mongo.Session
// implements it.
type MongoTransactor interface {
	WithTransaction(ctx context.Context, fn func(context.Context) (any, error), opts ...options.Lister[options.TransactionOptions]) (any, error)
}

// MongoTransaction runs fn inside a transaction on session. A standalone
// mongod cannot run transactions; there it logs a warning and runs fn again
// without one, so fn must be safe to repeat after an aborted transaction.
//
// operation names the work in errors: "<operation> transaction: ..." when the
// transaction fails, "<operation> without transaction: ..." when the fallback
// does.
func MongoTransaction(ctx context.Context, session MongoTransactor, operation string, fn func(context.Context) error) error {
	_, err := session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if err := fn(txCtx); err != nil {
			if isMongoTransactionCapabilityError(err) {
				// Hide the error's labels so WithTransaction does not retry
				// a transaction the server can never run.
				return nil, &mongoTransactionFallbackError{err: err}
			}
			return nil, err
		}
		return nil, nil
	})
	if err == nil {
		return nil
	}
	cause := err
	if fallbackErr, ok := errors.AsType[*mongoTransactionFallbackError](err); ok {
		cause = fallbackErr.err
	} else if !isMongoTransactionCapabilityError(err) {
		return fmt.Errorf("%s transaction: %w", operation, err)
	}
	slog.Warn("MongoDB transactions unavailable; falling back to non-transactional update", "operation", operation, "error", cause)
	if err := fn(ctx); err != nil {
		return fmt.Errorf("%s without transaction: %w", operation, errors.Join(cause, err))
	}
	return nil
}

type mongoTransactionFallbackError struct {
	err error
}

func (e *mongoTransactionFallbackError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

// isMongoTransactionCapabilityError reports whether err means the deployment
// cannot run transactions at all, as opposed to one transaction failing.
func isMongoTransactionCapabilityError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "transaction") {
		return false
	}
	// IllegalOperation (20) has other causes too, so it counts only together
	// with transaction wording.
	var commandErr mongo.CommandError
	if errors.As(err, &commandErr) && commandErr.HasErrorCode(20) {
		return true
	}
	var labeled mongo.LabeledError
	if errors.As(err, &labeled) && labeled.HasErrorLabel("TransientTransactionError") {
		return strings.Contains(message, "not supported") ||
			strings.Contains(message, "not allowed") ||
			strings.Contains(message, "replica set")
	}
	return strings.Contains(message, "transaction numbers are only allowed on a replica set member or mongos")
}
