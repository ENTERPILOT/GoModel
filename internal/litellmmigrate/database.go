package litellmmigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Database holds the LiteLLM proxy objects the import reads: virtual keys,
// teams, users, organizations, and the budget rows they reference.
type Database struct {
	Keys          []dbKey
	Teams         []dbTeam
	Users         []dbUser
	Organizations []dbOrganization
	Budgets       []dbBudget
}

// dbLimits are the spend and rate limit columns LiteLLM repeats on keys,
// teams, users, and budget rows. A nil field is unset.
type dbLimits struct {
	MaxBudget           *float64 `json:"max_budget"`
	BudgetDuration      *string  `json:"budget_duration"`
	TPMLimit            *int64   `json:"tpm_limit"`
	RPMLimit            *int64   `json:"rpm_limit"`
	TPDLimit            *int64   `json:"tpd_limit"`
	MaxParallelRequests *int64   `json:"max_parallel_requests"`
}

type dbKey struct {
	dbLimits
	Token          string         `json:"token"`
	KeyName        *string        `json:"key_name"`
	KeyAlias       *string        `json:"key_alias"`
	Spend          float64        `json:"spend"`
	Expires        *dbTime        `json:"expires"`
	Models         []string       `json:"models"`
	UserID         *string        `json:"user_id"`
	TeamID         *string        `json:"team_id"`
	OrganizationID *string        `json:"organization_id"`
	BudgetID       *string        `json:"budget_id"`
	Blocked        *bool          `json:"blocked"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      *dbTime        `json:"created_at"`
}

type dbTeam struct {
	dbLimits
	TeamID         string         `json:"team_id"`
	TeamAlias      *string        `json:"team_alias"`
	OrganizationID *string        `json:"organization_id"`
	Spend          float64        `json:"spend"`
	Models         []string       `json:"models"`
	Blocked        *bool          `json:"blocked"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      *dbTime        `json:"created_at"`
}

type dbUser struct {
	dbLimits
	UserID    string   `json:"user_id"`
	UserAlias *string  `json:"user_alias"`
	UserEmail *string  `json:"user_email"`
	Spend     float64  `json:"spend"`
	Models    []string `json:"models"`
	CreatedAt *dbTime  `json:"created_at"`
}

type dbOrganization struct {
	OrganizationID    string   `json:"organization_id"`
	OrganizationAlias *string  `json:"organization_alias"`
	BudgetID          *string  `json:"budget_id"`
	Spend             float64  `json:"spend"`
	Models            []string `json:"models"`
	CreatedAt         *dbTime  `json:"created_at"`
}

type dbBudget struct {
	dbLimits
	BudgetID string `json:"budget_id"`
}

// dbTime parses PostgreSQL's JSON rendering of a timestamp without time
// zone, which LiteLLM stores in UTC.
type dbTime struct{ time.Time }

func (t *dbTime) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.999999999", raw)
	if err != nil {
		if parsed, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return fmt.Errorf("parse timestamp %q: %w", raw, err)
		}
	}
	t.Time = parsed.UTC()
	return nil
}

// ReadDatabase reads the LiteLLM objects from the PostgreSQL database at url
// inside a read-only transaction. Each row is read as JSON, so columns a
// LiteLLM version lacks are simply unset, and a missing table reads as empty.
func ReadDatabase(ctx context.Context, url string) (*Database, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to the LiteLLM database: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("read the LiteLLM database: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('"LiteLLM_VerificationToken"') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("read the LiteLLM database: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("no LiteLLM_VerificationToken table: not a LiteLLM proxy database")
	}

	db := &Database{}
	if err := readTable(ctx, tx, "LiteLLM_VerificationToken", &db.Keys); err != nil {
		return nil, err
	}
	if err := readTable(ctx, tx, "LiteLLM_TeamTable", &db.Teams); err != nil {
		return nil, err
	}
	if err := readTable(ctx, tx, "LiteLLM_UserTable", &db.Users); err != nil {
		return nil, err
	}
	if err := readTable(ctx, tx, "LiteLLM_OrganizationTable", &db.Organizations); err != nil {
		return nil, err
	}
	if err := readTable(ctx, tx, "LiteLLM_BudgetTable", &db.Budgets); err != nil {
		return nil, err
	}
	return db, nil
}

func readTable[T any](ctx context.Context, tx pgx.Tx, table string, out *[]T) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, `"`+table+`"`).Scan(&exists); err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	if !exists {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT row_to_json(t)::text FROM "`+table+`" t`)
	if err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("read %s: %w", table, err)
		}
		var item T
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return fmt.Errorf("decode %s row: %w", table, err)
		}
		*out = append(*out, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	return nil
}
