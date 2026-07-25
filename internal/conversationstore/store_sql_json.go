package conversationstore

import (
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// The three mutating operations here are the one place in this repository
// where SQLite and PostgreSQL genuinely diverge rather than merely spelling
// the same statement differently.
//
// Each one must mutate JSON server-side in a single UPDATE: two Responses
// turns completing concurrently must not overwrite each other's exchange.
// SQLite expresses that with the JSON1 functions (json_set, json_insert,
// json_remove over json_each), PostgreSQL with jsonb operators (jsonb_set,
// ||, - over jsonb_array_elements). There is no portable spelling, and
// rewriting either as read-modify-write in Go would drop the atomicity.
//
// So the SQL is dialect-specific and the surrounding logic is not: each
// builder returns a statement and its arguments, and the store applies the
// same rows-affected interpretation to both.

// jsonMutations builds the dialect-specific JSON mutation statements.
type jsonMutations interface {
	// mergeMetadata overlays metadata under $.conversation.metadata, refusing
	// the update when the merged result would exceed the pair limit.
	mergeMetadata(id string, patch []byte, now int64) (string, []any)

	// appendItems appends items unless any of their ids is already present.
	appendItems(id string, items []json.RawMessage, encoded []byte, now int64) (string, []any)

	// deleteItem removes the first item carrying targetItemID.
	deleteItem(id, targetItemID string, now int64) (string, []any)
}

func mutationsFor(dialect sqlx.Dialect) (jsonMutations, error) {
	switch dialect {
	case sqlx.SQLite:
		return sqliteMutations{}, nil
	case sqlx.PostgreSQL:
		return postgresMutations{}, nil
	default:
		return nil, fmt.Errorf("unsupported dialect %q", dialect)
	}
}

// sqliteMutations builds statements against SQLite's JSON1 functions.
type sqliteMutations struct{}

func (sqliteMutations) mergeMetadata(id string, patch []byte, now int64) (string, []any) {
	const query = `
		UPDATE conversation_snapshots SET data = json_set(
			data,
			'$.conversation.metadata',
			json_patch(COALESCE(json_extract(data, '$.conversation.metadata'), json('{}')), json(?))
		)
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
		AND (
			SELECT COUNT(*) FROM json_each(
				json_patch(COALESCE(json_extract(data, '$.conversation.metadata'), json('{}')), json(?))
			)
		) <= ?
	`
	return query, []any{string(patch), id, now, string(patch), core.MaxConversationMetadataPairs}
}

func (sqliteMutations) appendItems(id string, items []json.RawMessage, encoded []byte, now int64) (string, []any) {
	// One json_insert '$[#]' path per item, so the append is a single UPDATE.
	var expr strings.Builder
	expr.WriteString("json_insert(items")
	args := make([]any, 0, len(items)+3)
	for _, item := range items {
		expr.WriteString(", '$[#]', json(?)")
		args = append(args, string(item))
	}
	expr.WriteString(")")
	args = append(args, id, now, string(encoded))

	return "UPDATE conversation_snapshots SET items = " + expr.String() +
		` WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
			AND NOT EXISTS (
				SELECT 1
				FROM json_each(items) AS existing
				JOIN json_each(json(?)) AS added
				  ON json_extract(existing.value, '$.id') = json_extract(added.value, '$.id')
			)`, args
}

func (sqliteMutations) deleteItem(id, targetItemID string, now int64) (string, []any) {
	const query = `
		UPDATE conversation_snapshots
		SET items = json_remove(items, '$[' || (
			SELECT key FROM json_each(items)
			WHERE json_extract(value, '$.id') = ?
			LIMIT 1
		) || ']')
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
		AND EXISTS (
			SELECT 1 FROM json_each(items)
			WHERE json_extract(value, '$.id') = ?
		)
	`
	return query, []any{targetItemID, id, now, targetItemID}
}

// postgresMutations builds statements against PostgreSQL's jsonb operators.
type postgresMutations struct{}

func (postgresMutations) mergeMetadata(id string, patch []byte, now int64) (string, []any) {
	const query = `
		UPDATE conversation_snapshots SET data = jsonb_set(
			data::jsonb,
			'{conversation,metadata}',
			COALESCE(data::jsonb #> '{conversation,metadata}', '{}'::jsonb) || ?::jsonb
		)::text
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
		AND (
			SELECT COUNT(*)
			FROM jsonb_object_keys(
				COALESCE(data::jsonb #> '{conversation,metadata}', '{}'::jsonb) || ?::jsonb
			)
		) <= ?
	`
	return query, []any{string(patch), id, now, string(patch), core.MaxConversationMetadataPairs}
}

func (postgresMutations) appendItems(id string, _ []json.RawMessage, encoded []byte, now int64) (string, []any) {
	const query = `
		UPDATE conversation_snapshots SET items = items || ?::jsonb
		WHERE id = ? AND (expires_at = 0 OR expires_at > ?)
		AND NOT EXISTS (
			SELECT 1
			FROM jsonb_array_elements(items) AS existing(value)
			JOIN jsonb_array_elements(?::jsonb) AS added(value)
			  ON existing.value ->> 'id' = added.value ->> 'id'
		)
	`
	return query, []any{string(encoded), id, now, string(encoded)}
}

func (postgresMutations) deleteItem(id, targetItemID string, now int64) (string, []any) {
	const query = `
		UPDATE conversation_snapshots c
		SET items = c.items - (
			SELECT (element.ordinality - 1)::int
			FROM jsonb_array_elements(c.items) WITH ORDINALITY AS element(value, ordinality)
			WHERE element.value ->> 'id' = ?
			ORDER BY element.ordinality
			LIMIT 1
		)
		WHERE c.id = ?
		  AND (c.expires_at = 0 OR c.expires_at > ?)
		  AND EXISTS (
			SELECT 1
			FROM jsonb_array_elements(c.items) AS element(value)
			WHERE element.value ->> 'id' = ?
		  )
	`
	return query, []any{targetItemID, id, now, targetItemID}
}
