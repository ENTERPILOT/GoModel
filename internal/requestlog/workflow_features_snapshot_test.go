package requestlog

import (
	stdjson "encoding/json"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The request_logs flag of the snapshot was named audit through v0.2.5. Both
// keys are written, so a v0.2.5 dashboard or replica still finds audit, and
// entries that release stored (audit alone) still read.

func TestWorkflowFeaturesSnapshotJSON(t *testing.T) {
	data := LogData{WorkflowFeatures: &WorkflowFeaturesSnapshot{Cache: true, RequestLogs: true}}

	viaGoccy, err := json.Marshal(data)
	require.NoError(t, err)
	viaStd, err := stdjson.Marshal(data)
	require.NoError(t, err)
	for _, raw := range [][]byte{viaGoccy, viaStd} {
		assert.JSONEq(t,
			`{"workflow_features":{"cache":true,"request_logs":true,"audit":true,"usage":false,"budget":false,"guardrails":false,"failover":false}}`,
			string(raw))
	}

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "audit only, as v0.2.5 stored it", raw: `{"workflow_features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":false,"failover":true}}`, want: true},
		{name: "audit only, off", raw: `{"workflow_features":{"audit":false,"usage":true}}`, want: false},
		{name: "request_logs only", raw: `{"workflow_features":{"request_logs":true}}`, want: true},
		{name: "request_logs wins over audit", raw: `{"workflow_features":{"audit":true,"request_logs":false}}`, want: false},
		{name: "neither", raw: `{"workflow_features":{"cache":true}}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoded LogData
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &decoded))
			require.NotNil(t, decoded.WorkflowFeatures)
			assert.Equal(t, tt.want, decoded.WorkflowFeatures.RequestLogs)
		})
	}

	var none LogData
	require.NoError(t, json.Unmarshal([]byte(`{}`), &none))
	assert.Nil(t, none.WorkflowFeatures)
}

func TestWorkflowFeaturesSnapshotBSON(t *testing.T) {
	type document struct {
		WorkflowFeatures *WorkflowFeaturesSnapshot `bson:"workflow_features,omitempty"`
	}
	snapshot := WorkflowFeaturesSnapshot{Cache: true, RequestLogs: true, Failover: true}

	raw, err := bson.Marshal(document{WorkflowFeatures: &snapshot})
	require.NoError(t, err)
	var keys struct {
		WorkflowFeatures bson.M `bson:"workflow_features"`
	}
	require.NoError(t, bson.Unmarshal(raw, &keys))
	assert.Equal(t, bson.M{
		"cache": true, "request_logs": true, "audit": true,
		"usage": false, "budget": false, "guardrails": false, "failover": true,
	}, keys.WorkflowFeatures)

	var back document
	require.NoError(t, bson.Unmarshal(raw, &back))
	require.NotNil(t, back.WorkflowFeatures)
	assert.Equal(t, snapshot, *back.WorkflowFeatures)

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
			raw, err := bson.Marshal(bson.D{{Key: "workflow_features", Value: tt.features}})
			require.NoError(t, err)
			var decoded document
			require.NoError(t, bson.Unmarshal(raw, &decoded))
			require.NotNil(t, decoded.WorkflowFeatures)
			assert.Equal(t, tt.want, decoded.WorkflowFeatures.RequestLogs)
		})
	}

	var none document
	empty, err := bson.Marshal(bson.D{})
	require.NoError(t, err)
	require.NoError(t, bson.Unmarshal(empty, &none))
	assert.Nil(t, none.WorkflowFeatures)
}
