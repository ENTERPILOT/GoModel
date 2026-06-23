package virtualmodels

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// REMOVE-LATER (cleanup milestone: one release after virtual models ship).
// These self-contained readers read the legacy `aliases` and `model_overrides`
// tables/collections directly so the unified package no longer depends on the
// deleted aliases/modeloverrides packages. Once all environments run the unified
// store, delete this file along with seed.go.

// legacyAlias is the minimal projection of a legacy alias row.
type legacyAlias struct {
	Name           string
	TargetModel    string
	TargetProvider string
	Description    string
	Enabled        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// legacyOverride is the minimal projection of a legacy model_overrides row.
type legacyOverride struct {
	Selector     string
	ProviderName string
	Model        string
	UserPaths    []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (a legacyAlias) toRedirect() VirtualModel {
	return VirtualModel{
		Source:      a.Name,
		Targets:     []Target{{Provider: a.TargetProvider, Model: a.TargetModel}},
		Description: a.Description,
		Enabled:     a.Enabled,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}

func (o legacyOverride) toPolicy() VirtualModel {
	return VirtualModel{
		Source:       o.Selector,
		ProviderName: o.ProviderName,
		Model:        o.Model,
		UserPaths:    append([]string(nil), o.UserPaths...),
		Enabled:      true,
		CreatedAt:    o.CreatedAt,
		UpdatedAt:    o.UpdatedAt,
	}
}

// --- SQLite ---

func readLegacyAliasesSQLite(ctx context.Context, db *sql.DB) ([]legacyAlias, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name, target_model, target_provider, description, enabled, created_at, updated_at
		FROM aliases ORDER BY name ASC
	`)
	if err != nil {
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	result := make([]legacyAlias, 0)
	for rows.Next() {
		var a legacyAlias
		var enabled int
		var createdAt, updatedAt int64
		if err := rows.Scan(&a.Name, &a.TargetModel, &a.TargetProvider, &a.Description, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		a.CreatedAt = time.Unix(createdAt, 0).UTC()
		a.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		result = append(result, a)
	}
	return result, rows.Err()
}

func readLegacyOverridesSQLite(ctx context.Context, db *sql.DB) ([]legacyOverride, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT selector, provider_name, model, user_paths, created_at, updated_at
		FROM model_overrides ORDER BY selector ASC
	`)
	if err != nil {
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	result := make([]legacyOverride, 0)
	for rows.Next() {
		var o legacyOverride
		var userPaths string
		var createdAt, updatedAt int64
		if err := rows.Scan(&o.Selector, &o.ProviderName, &o.Model, &userPaths, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(userPaths), &o.UserPaths); err != nil {
			return nil, err
		}
		o.CreatedAt = time.Unix(createdAt, 0).UTC()
		o.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		result = append(result, o)
	}
	return result, rows.Err()
}

// --- PostgreSQL ---

func readLegacyAliasesPostgreSQL(ctx context.Context, pool *pgxpool.Pool) ([]legacyAlias, error) {
	rows, err := pool.Query(ctx, `
		SELECT name, target_model, target_provider, description, enabled, created_at, updated_at
		FROM aliases ORDER BY name ASC
	`)
	if err != nil {
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	result := make([]legacyAlias, 0)
	for rows.Next() {
		var a legacyAlias
		var createdAt, updatedAt int64
		if err := rows.Scan(&a.Name, &a.TargetModel, &a.TargetProvider, &a.Description, &a.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(createdAt, 0).UTC()
		a.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	return result, nil
}

func readLegacyOverridesPostgreSQL(ctx context.Context, pool *pgxpool.Pool) ([]legacyOverride, error) {
	rows, err := pool.Query(ctx, `
		SELECT selector, provider_name, model, user_paths, created_at, updated_at
		FROM model_overrides ORDER BY selector ASC
	`)
	if err != nil {
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	result := make([]legacyOverride, 0)
	for rows.Next() {
		var o legacyOverride
		var userPaths []byte
		var createdAt, updatedAt int64
		if err := rows.Scan(&o.Selector, &o.ProviderName, &o.Model, &userPaths, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(userPaths, &o.UserPaths); err != nil {
			return nil, err
		}
		o.CreatedAt = time.Unix(createdAt, 0).UTC()
		o.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		result = append(result, o)
	}
	if err := rows.Err(); err != nil {
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	return result, nil
}

// --- MongoDB ---

type mongoLegacyAliasDoc struct {
	Name           string    `bson:"_id"`
	TargetModel    string    `bson:"target_model"`
	TargetProvider string    `bson:"target_provider,omitempty"`
	Description    string    `bson:"description,omitempty"`
	Enabled        bool      `bson:"enabled"`
	CreatedAt      time.Time `bson:"created_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

type mongoLegacyOverrideDoc struct {
	Selector     string    `bson:"_id"`
	ProviderName string    `bson:"provider_name,omitempty"`
	Model        string    `bson:"model,omitempty"`
	UserPaths    []string  `bson:"user_paths,omitempty"`
	CreatedAt    time.Time `bson:"created_at"`
	UpdatedAt    time.Time `bson:"updated_at"`
}

func readLegacyAliasesMongo(ctx context.Context, db *mongo.Database) ([]legacyAlias, error) {
	cursor, err := db.Collection("aliases").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	result := make([]legacyAlias, 0)
	for cursor.Next(ctx) {
		var doc mongoLegacyAliasDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		result = append(result, legacyAlias{
			Name:           doc.Name,
			TargetModel:    doc.TargetModel,
			TargetProvider: doc.TargetProvider,
			Description:    doc.Description,
			Enabled:        doc.Enabled,
			CreatedAt:      doc.CreatedAt.UTC(),
			UpdatedAt:      doc.UpdatedAt.UTC(),
		})
	}
	return result, cursor.Err()
}

func readLegacyOverridesMongo(ctx context.Context, db *mongo.Database) ([]legacyOverride, error) {
	cursor, err := db.Collection("model_overrides").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	result := make([]legacyOverride, 0)
	for cursor.Next(ctx) {
		var doc mongoLegacyOverrideDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		result = append(result, legacyOverride{
			Selector:     doc.Selector,
			ProviderName: doc.ProviderName,
			Model:        doc.Model,
			UserPaths:    append([]string(nil), doc.UserPaths...),
			CreatedAt:    doc.CreatedAt.UTC(),
			UpdatedAt:    doc.UpdatedAt.UTC(),
		})
	}
	return result, cursor.Err()
}

// isMissingTableError reports whether err indicates the queried table does not
// exist, so a missing legacy table is treated as zero rows rather than failing.
// PostgreSQL surfaces this as a *pgconn.PgError (code 42P01); textual matching
// keeps this self-contained without importing pgconn.
func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "no such table"):
		return true
	case strings.Contains(message, "does not exist") && strings.Contains(message, "relation"):
		return true
	case strings.Contains(message, "undefined table"):
		return true
	default:
		return false
	}
}
