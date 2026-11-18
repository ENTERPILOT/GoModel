package workflows

import (
	"context"
	stdjson "encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The request_logs feature flag was named audit through v0.2.5. These tests
// cover the transition: both keys are written, either is read, and workflow
// hashes are the ones v0.2.5 computed.

func TestFeatureFlagsJSON_ReadsRequestLogsThenAudit(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "audit only, as v0.2.5 wrote it", raw: `{"cache":true,"audit":true,"usage":true,"guardrails":false}`, want: true},
		{name: "audit only, off", raw: `{"audit":false}`, want: false},
		{name: "request_logs only", raw: `{"request_logs":true}`, want: true},
		{name: "request_logs wins over audit", raw: `{"audit":true,"request_logs":false}`, want: false},
		{name: "request_logs wins over audit, reversed", raw: `{"request_logs":true,"audit":false}`, want: true},
		{name: "neither", raw: `{"cache":true}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var viaGoccy, viaStd FeatureFlags
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &viaGoccy))
			require.NoError(t, stdjson.Unmarshal([]byte(tt.raw), &viaStd))
			assert.Equal(t, tt.want, viaGoccy.RequestLogs)
			assert.Equal(t, viaGoccy, viaStd)
		})
	}
}

func TestFeatureFlagsJSON_WritesBothKeys(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		flags := FeatureFlags{Cache: true, RequestLogs: enabled, Usage: true}

		viaGoccy, err := json.Marshal(flags)
		require.NoError(t, err)
		viaStd, err := stdjson.Marshal(flags)
		require.NoError(t, err)
		assert.JSONEq(t, string(viaGoccy), string(viaStd))

		var keys map[string]any
		require.NoError(t, json.Unmarshal(viaGoccy, &keys))
		assert.Equal(t, enabled, keys["request_logs"])
		assert.Equal(t, enabled, keys["audit"], "v0.2.5 reads only audit")
		assert.NotContains(t, keys, "budget", "an unset optional flag stays out")

		// Nested in a payload, as the stores and the admin API write it.
		raw, err := json.Marshal(Payload{SchemaVersion: 2, Features: flags})
		require.NoError(t, err)
		var payload struct {
			Features map[string]any `json:"features"`
		}
		require.NoError(t, json.Unmarshal(raw, &payload))
		assert.Equal(t, enabled, payload.Features["request_logs"])
		assert.Equal(t, enabled, payload.Features["audit"])

		var back Payload
		require.NoError(t, json.Unmarshal(raw, &back))
		assert.Equal(t, flags, back.Features)
	}
}

func TestFeatureFlagsBSON_ReadsRequestLogsThenAudit(t *testing.T) {
	tests := []struct {
		name     string
		features bson.D
		want     bool
	}{
		{name: "audit only, as v0.2.5 stored it", features: bson.D{{Key: "cache", Value: true}, {Key: "audit", Value: true}}, want: true},
		{name: "request_logs only", features: bson.D{{Key: "request_logs", Value: true}}, want: true},
		{name: "request_logs wins over audit", features: bson.D{{Key: "audit", Value: true}, {Key: "request_logs", Value: false}}, want: false},
		{name: "neither", features: bson.D{{Key: "cache", Value: true}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := bson.Marshal(bson.D{{Key: "schema_version", Value: 2}, {Key: "features", Value: tt.features}})
			require.NoError(t, err)
			var payload Payload
			require.NoError(t, bson.Unmarshal(raw, &payload))
			assert.Equal(t, tt.want, payload.Features.RequestLogs)
		})
	}
}

func TestFeatureFlagsBSON_WritesBothKeys(t *testing.T) {
	budget := false
	flags := FeatureFlags{Cache: true, RequestLogs: true, Usage: true, Budget: &budget}
	raw, err := bson.Marshal(mongoVersionDocument{ID: "wf-1", WorkflowPayload: Payload{SchemaVersion: 2, Features: flags}})
	require.NoError(t, err)

	var doc struct {
		WorkflowPayload struct {
			Features bson.M `bson:"features"`
		} `bson:"workflow_payload"`
	}
	require.NoError(t, bson.Unmarshal(raw, &doc))
	assert.Equal(t, true, doc.WorkflowPayload.Features["request_logs"])
	assert.Equal(t, true, doc.WorkflowPayload.Features["audit"], "v0.2.5 reads only audit")
	assert.Equal(t, false, doc.WorkflowPayload.Features["budget"])
	assert.NotContains(t, doc.WorkflowPayload.Features, "failover")

	var back mongoVersionDocument
	require.NoError(t, bson.Unmarshal(raw, &back))
	assert.Equal(t, flags, back.WorkflowPayload.Features)
}

// The hashes below were computed by v0.2.5 for the JSON beside them. They must
// never change: stored versions carry them, and the managed default workflow
// is matched by hash at every start, by old and new replicas alike.
func TestNormalizePayload_KeepsV025WorkflowHashes(t *testing.T) {
	off := false
	tests := []struct {
		name    string
		payload Payload
		v025    string
		want    string
	}{
		{
			name:    "all features on",
			payload: Payload{Features: FeatureFlags{Cache: true, RequestLogs: true, Usage: true, Guardrails: true}},
			v025:    `{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true}}`,
			want:    "7c1bca92a914d1cfe6c085a75bae53830e896b2e71250d3165d9d2bfdb391177",
		},
		{
			name:    "request logs off",
			payload: Payload{Features: FeatureFlags{Cache: true, Usage: true, Budget: &off}},
			v025:    `{"schema_version":2,"features":{"cache":true,"audit":false,"usage":true,"budget":false,"guardrails":false,"failover":true}}`,
			want:    "54bc46ecd00c09b33e6a41c5ebf019120f2782a96fcc2bb5fd484dcc4f781445",
		},
		{
			name:    "schema version 1",
			payload: Payload{SchemaVersion: 1, Features: FeatureFlags{RequestLogs: true}, Guardrails: []GuardrailStep{{Ref: "pii", Step: 10}}},
			v025:    `{"schema_version":1,"features":{"cache":false,"audit":true,"usage":false,"budget":true,"guardrails":false,"failover":true},"guardrails":[{"ref":"pii","step":10}]}`,
			want:    "4a591c06e580f1ce48e93b1674650a5ecb1e78ba80137dee8f28dd00438e61b1",
		},
		{
			name: "steps",
			payload: Payload{
				Features: FeatureFlags{RequestLogs: true, Guardrails: true, Failover: &off},
				Steps:    []Step{{Ref: "pii", Phase: "response", Step: 20}, {Ref: "judge", Step: 10}},
			},
			v025: `{"schema_version":2,"features":{"cache":false,"audit":true,"usage":false,"budget":true,"guardrails":true,"failover":false},"steps":[{"ref":"judge","phase":"prompt","step":10},{"ref":"pii","phase":"response","step":20}]}`,
			want: "ca6e868273872ae16642309ddf262770697771236fa3ca2f018a65c5635678f9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, hash, err := normalizePayload(tt.payload)
			require.NoError(t, err)
			assert.Equal(t, tt.want, hash)

			// A payload v0.2.5 stored hashes to the same value once decoded.
			var stored Payload
			require.NoError(t, json.Unmarshal([]byte(tt.v025), &stored))
			_, storedHash, err := normalizePayload(stored)
			require.NoError(t, err)
			assert.Equal(t, tt.want, storedHash)
		})
	}
}

// hashedPayload repeats Payload's fields by hand, so a field added to one and
// not the other would silently drop out of the hash.
func TestHashedPayloadCoversEveryPayloadField(t *testing.T) {
	assertSameJSONFields(t, reflect.TypeFor[Payload](), reflect.TypeFor[hashedPayload](), nil)
	assertSameJSONFields(t, reflect.TypeFor[FeatureFlags](), reflect.TypeFor[hashedFeatureFlags](),
		map[string]string{"request_logs": "audit"})
}

func assertSameJSONFields(t *testing.T, source, hashed reflect.Type, renamed map[string]string) {
	t.Helper()
	require.Equal(t, source.NumField(), hashed.NumField(), "%s and %s differ in field count", source, hashed)
	for i := range source.NumField() {
		want := source.Field(i).Tag.Get("json")
		if legacy, ok := renamed[want]; ok {
			want = legacy
		}
		assert.Equal(t, want, hashed.Field(i).Tag.Get("json"), "field %d of %s", i, hashed)
	}
}

// An upgrade must leave the managed default workflow v0.2.5 published alone.
// Publishing a new version would be repeated by every v0.2.5 replica that
// starts afterwards, each side replacing the other's version.
func TestStoreEnsureManagedDefaultGlobalKeepsV025Version(t *testing.T) {
	const (
		v025Payload = `{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true}}`
		v025Hash    = "7c1bca92a914d1cfe6c085a75bae53830e896b2e71250d3165d9d2bfdb391177"
	)
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		createdAt := time.Now().UTC().Truncate(time.Second)

		switch s := store.(type) {
		case *SQLStore:
			_, err := s.db.Exec(ctx, insertVersionSQL,
				"wf-v025", nil, nil, nil, "global", 1, true, true,
				ManagedDefaultGlobalName, ManagedDefaultGlobalDescription,
				v025Payload, v025Hash, createdAt.Unix())
			require.NoError(t, err)
		case *MongoDBStore:
			_, err := s.collection.InsertOne(ctx, bson.D{
				{Key: "_id", Value: "wf-v025"},
				{Key: "scope_key", Value: "global"},
				{Key: "version", Value: 1},
				{Key: "active", Value: true},
				{Key: "managed_default", Value: true},
				{Key: "name", Value: ManagedDefaultGlobalName},
				{Key: "description", Value: ManagedDefaultGlobalDescription},
				{Key: "workflow_payload", Value: bson.D{
					{Key: "schema_version", Value: 2},
					{Key: "features", Value: bson.D{
						{Key: "cache", Value: true},
						{Key: "audit", Value: true},
						{Key: "usage", Value: true},
						{Key: "budget", Value: true},
						{Key: "guardrails", Value: true},
						{Key: "failover", Value: true},
					}},
				}},
				{Key: "workflow_hash", Value: v025Hash},
				{Key: "created_at", Value: createdAt},
			})
			require.NoError(t, err)
		default:
			t.Fatalf("unexpected store %T", store)
		}

		input, _, hash, err := normalizeCreateInput(CreateInput{
			Name:        ManagedDefaultGlobalName,
			Description: ManagedDefaultGlobalDescription,
			Payload:     Payload{SchemaVersion: 2, Features: FeatureFlags{Cache: true, RequestLogs: true, Usage: true, Guardrails: true}},
			Managed:     true,
			Activate:    true,
		})
		require.NoError(t, err)
		require.Equal(t, v025Hash, hash)

		published, err := store.EnsureManagedDefaultGlobal(ctx, input, hash)
		require.NoError(t, err)
		assert.Nil(t, published, "the v0.2.5 version already matches")

		active, err := store.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, "wf-v025", active[0].ID)
		assert.True(t, active[0].Payload.Features.RequestLogs, "audit: true reads as request logs on")
	})
}

// A version this release stores must still read as request logs on to a
// v0.2.5 replica sharing the database, which looks at audit alone.
func TestStoreWritesAuditAliasForV025Replicas(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		created, err := store.Create(ctx, CreateInput{
			Name:     "logged",
			Activate: true,
			Payload:  Payload{SchemaVersion: 2, Features: FeatureFlags{RequestLogs: true}},
		})
		require.NoError(t, err)

		var features map[string]any
		switch s := store.(type) {
		case *SQLStore:
			var raw string
			require.NoError(t, s.db.QueryRow(ctx,
				`SELECT workflow_payload FROM workflow_versions WHERE id = ?`, created.ID).Scan(&raw))
			var payload struct {
				Features map[string]any `json:"features"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &payload))
			features = payload.Features
		case *MongoDBStore:
			var doc struct {
				Payload struct {
					Features bson.M `bson:"features"`
				} `bson:"workflow_payload"`
			}
			require.NoError(t, s.collection.FindOne(ctx, bson.D{{Key: "_id", Value: created.ID}}).Decode(&doc))
			features = doc.Payload.Features
		default:
			t.Fatalf("unexpected store %T", store)
		}
		assert.Equal(t, true, features["audit"])
		assert.Equal(t, true, features["request_logs"])

		stored, err := store.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.True(t, stored.Payload.Features.RequestLogs)
	})
}
