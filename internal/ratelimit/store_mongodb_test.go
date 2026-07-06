package ratelimit

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestIsOnlyDuplicateKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "single duplicate key write error",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000, Message: "E11000 duplicate key error"}},
				},
			},
			want: true,
		},
		{
			name: "all duplicate key write errors",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000}},
					{WriteError: mongo.WriteError{Code: 11000}},
				},
			},
			want: true,
		},
		{
			name: "mixed write errors keep failing",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000}},
					{WriteError: mongo.WriteError{Code: 121, Message: "document validation failure"}},
				},
			},
			want: false,
		},
		{
			name: "write concern error keeps failing",
			err: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{
					{WriteError: mongo.WriteError{Code: 11000}},
				},
				WriteConcernError: &mongo.WriteConcernError{Code: 64},
			},
			want: false,
		},
		{
			name: "empty bulk exception",
			err:  mongo.BulkWriteException{},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection reset"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOnlyDuplicateKeyErrors(tt.err); got != tt.want {
				t.Fatalf("isOnlyDuplicateKeyErrors() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDuplicateKeyErrorsOnConfigRulesOnly(t *testing.T) {
	configRule := Rule{Scope: ScopeUserPath, Subject: "/seed", PeriodSeconds: PeriodMinuteSeconds, Source: SourceConfig}
	manualRule := Rule{Scope: ScopeUserPath, Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, Source: SourceManual}
	dupAt := func(indexes ...int) mongo.BulkWriteException {
		exc := mongo.BulkWriteException{}
		for _, index := range indexes {
			exc.WriteErrors = append(exc.WriteErrors, mongo.BulkWriteError{
				WriteError: mongo.WriteError{Index: index, Code: 11000},
			})
		}
		return exc
	}

	tests := []struct {
		name  string
		err   error
		rules []Rule
		want  bool
	}{
		{
			name:  "duplicate on config rule is the intended shadowing",
			err:   dupAt(0),
			rules: []Rule{configRule},
			want:  true,
		},
		{
			name:  "duplicate on manual rule is a real insert race",
			err:   dupAt(1),
			rules: []Rule{configRule, manualRule},
			want:  false,
		},
		{
			name:  "mixed batch with only config duplicates passes",
			err:   dupAt(0),
			rules: []Rule{configRule, manualRule},
			want:  true,
		},
		{
			name:  "index out of range keeps failing",
			err:   dupAt(5),
			rules: []Rule{configRule},
			want:  false,
		},
		{
			name:  "non duplicate-key code keeps failing",
			err:   mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{{WriteError: mongo.WriteError{Index: 0, Code: 121}}}},
			rules: []Rule{configRule},
			want:  false,
		},
		{
			name:  "unrelated error keeps failing",
			err:   errors.New("network down"),
			rules: []Rule{configRule},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := duplicateKeyErrorsOnConfigRulesOnly(tt.err, tt.rules); got != tt.want {
				t.Fatalf("duplicateKeyErrorsOnConfigRulesOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestClassifyBulkWriteError pins the upsert error-handling contract: config
// duplicates are benign shadowing, manual duplicates earn one retry, and
// everything else fails. The retry itself lands as a plain update because the
// conflicting documents exist by then.
func TestClassifyBulkWriteError(t *testing.T) {
	configRule := Rule{Scope: ScopeUserPath, Subject: "/seed", PeriodSeconds: PeriodMinuteSeconds, Source: SourceConfig}
	manualRule := Rule{Scope: ScopeUserPath, Subject: "/manual", PeriodSeconds: PeriodMinuteSeconds, Source: SourceManual}
	dup := func(index int) mongo.BulkWriteException {
		return mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{
			{WriteError: mongo.WriteError{Index: index, Code: 11000}},
		}}
	}

	tests := []struct {
		name  string
		err   error
		rules []Rule
		want  bulkWriteOutcome
	}{
		{"success", nil, []Rule{manualRule}, bulkWriteOK},
		{"config duplicate is shadowing", dup(0), []Rule{configRule}, bulkWriteShadowedByManual},
		{"manual duplicate is a race worth retrying", dup(1), []Rule{configRule, manualRule}, bulkWriteRetryManualRace},
		{"non-duplicate error fails", mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{
			{WriteError: mongo.WriteError{Index: 0, Code: 121}},
		}}, []Rule{manualRule}, bulkWriteFailed},
		{"unrelated error fails", errors.New("network down"), []Rule{manualRule}, bulkWriteFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyBulkWriteError(tt.err, tt.rules); got != tt.want {
				t.Fatalf("classifyBulkWriteError() = %d, want %d", got, tt.want)
			}
		})
	}
}
