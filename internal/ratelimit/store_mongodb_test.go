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
