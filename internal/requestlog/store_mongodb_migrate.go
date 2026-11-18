package requestlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const requestLogCollectionName = "request_logs"

// The request log was stored as the "audit log" up to v0.2.5.
// legacyStagingCollection holds a legacy collection while its documents move
// into the request log collection.
const (
	legacyCollectionName    = "audit_logs"
	legacyStagingCollection = "audit_logs_absorbing"
)

// MongoDB server error codes the migration expects from a concurrent start.
const (
	mongoNamespaceNotFound = 26
	mongoNamespaceExists   = 48
)

// requestLogCollection returns the request log collection, after moving a
// database written by v0.2.5 or older onto its name (adoptLegacyCollection).
//
// A deployment that cannot rename the collection — a role without the
// privilege, a sharded collection on an older server — keeps its request log
// under the legacy name rather than failing to start.
func requestLogCollection(ctx context.Context, database *mongo.Database) *mongo.Collection {
	err := adoptLegacyCollection(ctx, database)
	if err == nil {
		return database.Collection(requestLogCollectionName)
	}
	if names, listErr := requestLogCollectionNames(ctx, database); listErr == nil &&
		slices.Contains(names, legacyCollectionName) && !slices.Contains(names, requestLogCollectionName) {
		slog.Warn("requestlog: could not rename the audit_logs collection to request_logs; the request log stays in audit_logs",
			"error", err)
		return database.Collection(legacyCollectionName)
	}
	slog.Warn("requestlog: could not move the audit_logs collection into request_logs; its entries stay out of the request log until a later start",
		"error", err)
	return database.Collection(requestLogCollectionName)
}

// adoptLegacyCollection moves a database written by v0.2.5 or older onto the
// request_logs collection. It runs on every start and does nothing once the
// legacy collection is gone.
//
// A legacy collection is renamed, which keeps its indexes. When request_logs
// exists as well, a binary on the older release wrote after the rename — a
// replica not upgraded yet, or a rollback — and MongoDB recreated the legacy
// collection for it. Those documents are moved over: the collection is first
// renamed aside, so a replica that is still writing starts a new one instead
// of losing documents to the drop, and each later start picks that one up.
func adoptLegacyCollection(ctx context.Context, database *mongo.Database) error {
	names, err := requestLogCollectionNames(ctx, database)
	if err != nil {
		return err
	}
	legacy := slices.Contains(names, legacyCollectionName)
	staging := slices.Contains(names, legacyStagingCollection)
	if !legacy && !staging {
		return nil
	}

	if legacy && !staging && !slices.Contains(names, requestLogCollectionName) {
		err := renameCollection(ctx, database, legacyCollectionName, requestLogCollectionName)
		if err == nil {
			slog.Info("requestlog: renamed the audit_logs collection to request_logs")
			return nil
		}
		if isMongoError(err, mongoNamespaceNotFound) {
			return nil // another replica renamed it first
		}
		if !isMongoError(err, mongoNamespaceExists) {
			return err
		}
		// Another replica renamed it first and an older one has written since.
	}

	// A staging collection is left over from a start that was interrupted.
	if staging {
		if err := absorbStagedCollection(ctx, database); err != nil {
			return err
		}
	}
	if !legacy {
		return nil
	}
	err = renameCollection(ctx, database, legacyCollectionName, legacyStagingCollection)
	if isMongoError(err, mongoNamespaceNotFound) || isMongoError(err, mongoNamespaceExists) {
		return nil // another replica is moving it
	}
	if err != nil {
		return err
	}
	return absorbStagedCollection(ctx, database)
}

// absorbStagedCollection merges the staged legacy documents into the request
// log collection and drops the staging collection. Entries keep their ids, so
// a merge repeated after an interruption changes nothing.
func absorbStagedCollection(ctx context.Context, database *mongo.Database) error {
	staged := database.Collection(legacyStagingCollection)
	cursor, err := staged.Aggregate(ctx, mongo.Pipeline{{{Key: "$merge", Value: bson.D{
		{Key: "into", Value: requestLogCollectionName},
		{Key: "whenMatched", Value: "keepExisting"},
		{Key: "whenNotMatched", Value: "insert"},
	}}}})
	if err != nil {
		return fmt.Errorf("merge %s into %s: %w", legacyStagingCollection, requestLogCollectionName, err)
	}
	if err := cursor.Close(ctx); err != nil {
		return fmt.Errorf("merge %s into %s: %w", legacyStagingCollection, requestLogCollectionName, err)
	}
	if err := staged.Drop(ctx); err != nil {
		return fmt.Errorf("drop %s: %w", legacyStagingCollection, err)
	}
	slog.Info("requestlog: moved audit_logs entries written by an older release into request_logs")
	return nil
}

// requestLogCollectionNames lists which of the request log collection and its
// legacy forms exist.
func requestLogCollectionNames(ctx context.Context, database *mongo.Database) ([]string, error) {
	names, err := database.ListCollectionNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$in", Value: bson.A{
		requestLogCollectionName, legacyCollectionName, legacyStagingCollection,
	}}}}})
	if err != nil {
		return nil, fmt.Errorf("list request log collections: %w", err)
	}
	return names, nil
}

// renameCollection renames a collection within database. The command only
// runs against the admin database, whichever database the collection is in.
func renameCollection(ctx context.Context, database *mongo.Database, from, to string) error {
	return database.Client().Database("admin").RunCommand(ctx, bson.D{
		{Key: "renameCollection", Value: database.Name() + "." + from},
		{Key: "to", Value: database.Name() + "." + to},
	}).Err()
}

func isMongoError(err error, code int) bool {
	var commandErr mongo.CommandError
	return errors.As(err, &commandErr) && commandErr.HasErrorCode(code)
}
