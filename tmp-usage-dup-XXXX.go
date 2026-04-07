package main

import (
    "context"
    "database/sql"
    "fmt"
    "time"

    _ "github.com/mattn/go-sqlite3"
    "gomodel/internal/usage"
)

func main() {
    db, err := sql.Open("sqlite3", ":memory:")
    if err != nil { panic(err) }
    defer db.Close()
    store, err := usage.NewSQLiteStore(db, 0)
    if err != nil { panic(err) }
    ctx := context.Background()
    now := time.Now().UTC()
    entries := []*usage.UsageEntry{
        {ID:"old", RequestID:"r1", ProviderID:"p1", Timestamp:now.Add(-time.Hour), Model:"gpt-4o", Provider:"openai", Endpoint:"/v1/chat/completions", InputTokens:1, OutputTokens:1, TotalTokens:2},
        {ID:"new", RequestID:"r2", ProviderID:"p1", Timestamp:now, Model:"gpt-4o", Provider:"openai", ProviderName:"openai", Endpoint:"/v1/chat/completions", InputTokens:2, OutputTokens:2, TotalTokens:4},
    }
    if err := store.WriteBatch(ctx, entries); err != nil { panic(err) }
    reader, err := usage.NewSQLiteReader(db)
    if err != nil { panic(err) }
    rows, err := reader.GetUsageByModel(ctx, usage.UsageQueryParams{StartDate: now.AddDate(0,0,-1), EndDate: now.AddDate(0,0,1)})
    if err != nil { panic(err) }
    fmt.Printf("rows=%d\n", len(rows))
    for _, r := range rows { fmt.Printf("%#v\n", r) }
}
