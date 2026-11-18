package requestlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const requestLogCollectionName = "request_logs"

// The request log was stored as the "audit log" up to v0.2.5. A legacy
// collection is set aside under legacyStagingPrefix plus a unique suffix while
// its documents move into the request log collection.
const (
	legacyCollectionName = "audit_logs"
	legacyStagingPrefix  = "audit_logs_absorbing_"
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
//
// Replicas starting together may run this at once. Each sets the legacy
// collection aside under a name of its own, and nothing writes to a collection
// once it is set aside, so whoever drops one has merged all of it.
func adoptLegacyCollection(ctx context.Context, database *mongo.Database) error {
	names, err := requestLogCollectionNames(ctx, database)
	if err != nil {
		return err
	}
	legacy := slices.Contains(names, legacyCollectionName)

	if legacy && !slices.Contains(names, requestLogCollectionName) {
		err := renameCollection(ctx, database, legacyCollectionName, requestLogCollectionName)
		switch {
		case err == nil:
			slog.Info("requestlog: renamed the audit_logs collection to request_logs")
			legacy = false
		case isMongoError(err, mongoNamespaceNotFound):
			legacy = false // another replica renamed it first
		case isMongoError(err, mongoNamespaceExists):
			// Another replica renamed it first and an older one has written since.
		default:
			return err
		}
	}

	// Staging collections are left over from a start that was interrupted, or
	// belong to a replica doing this right now.
	for _, name := range names {
		if strings.HasPrefix(name, legacyStagingPrefix) {
			if err := absorbStagedCollection(ctx, database, name); err != nil {
				return err
			}
		}
	}
	if !legacy {
		return nil
	}
	staging := legacyStagingPrefix + bson.NewObjectID().Hex()
	err = renameCollection(ctx, database, legacyCollectionName, staging)
	if isMongoError(err, mongoNamespaceNotFound) {
		return nil // another replica set it aside first
	}
	if err != nil {
		return err
	}
	return absorbStagedCollection(ctx, database, staging)
}

// absorbStagedCollection merges a staged legacy collection into the request
// log collection and drops it. Entries keep their ids, so a merge repeated
// after an interruption, or run by two replicas, changes nothing.
func absorbStagedCollection(ctx context.Context, database *mongo.Database, name string) error {
	staged := database.Collection(name)
	err := mergeIntoRequestLog(ctx, staged)
	if err != nil {
		// Another replica that finished merging this collection dropped it.
		if names, listErr := requestLogCollectionNames(ctx, database); listErr == nil && !slices.Contains(names, name) {
			return nil
		}
		return fmt.Errorf("merge %s into %s: %w", name, requestLogCollectionName, err)
	}
	if err := staged.Drop(ctx); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	slog.Info("requestlog: moved audit_logs entries written by an older release into request_logs")
	return nil
}

func mergeIntoRequestLog(ctx context.Context, staged *mongo.Collection) error {
	cursor, err := staged.Aggregate(ctx, mongo.Pipeline{{{Key: "$merge", Value: bson.D{
		{Key: "into", Value: requestLogCollectionName},
		{Key: "whenMatched", Value: "keepExisting"},
		{Key: "whenNotMatched", Value: "insert"},
	}}}})
	if err != nil {
		return err
	}
	return cursor.Close(ctx)
}

// requestLogCollectionNames lists which of the request log collection, the
// legacy collection and its staging collections exist.
func requestLogCollectionNames(ctx context.Context, database *mongo.Database) ([]string, error) {
	names, err := database.ListCollectionNames(ctx, bson.D{{Key: "name", Value: bson.D{{
		Key:   "$regex",
		Value: "^(" + requestLogCollectionName + "|" + legacyCollectionName + "|" + legacyStagingPrefix + ".*)$",
	}}}})
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
