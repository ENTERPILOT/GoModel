package storage

import "testing"

func TestResolveMongoDatabase(t *testing.T) {
	tests := []struct {
		name string
		cfg  MongoDBConfig
		want string
	}{
		{
			name: "explicit database wins over URL path",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/from_url", Database: "explicit"},
			want: "explicit",
		},
		{
			name: "database from URL path",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/from_url"},
			want: "from_url",
		},
		{
			name: "URL path with query options",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/mydb?retryWrites=true&w=majority"},
			want: "mydb",
		},
		{
			name: "srv scheme",
			cfg:  MongoDBConfig{URL: "mongodb+srv://user:pass@cluster.example.com/appdb"},
			want: "appdb",
		},
		{
			name: "multi-host URL",
			cfg:  MongoDBConfig{URL: "mongodb://h1:27017,h2:27017/replicadb"},
			want: "replicadb",
		},
		{
			name: "no database in URL falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017"},
			want: DefaultMongoDatabase,
		},
		{
			name: "trailing slash only falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/"},
			want: DefaultMongoDatabase,
		},
		{
			name: "database with trailing slash is trimmed",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/mydb/"},
			want: "mydb",
		},
		{
			name: "multi-segment path falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/a/b"},
			want: DefaultMongoDatabase,
		},
		{
			name: "unparseable URL falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://[::1"},
			want: DefaultMongoDatabase,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveMongoDatabase(tt.cfg); got != tt.want {
				t.Errorf("resolveMongoDatabase(%+v) = %q, want %q", tt.cfg, got, tt.want)
			}
		})
	}
}
