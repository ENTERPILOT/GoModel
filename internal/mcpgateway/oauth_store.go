package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// oauthSecretKind and oauthSecretField name sealed OAuth records in their
// AAD, so a record cannot be moved to another server or table.
const (
	oauthSecretKind  = "mcp_server_oauth" //nolint:gosec // An AAD label, not a credential.
	oauthSecretField = "data"
)

// oauthRecord is the OAuth client registration and token set the gateway
// holds for one upstream server after an operator signed in. It is runtime
// state, never config, and never leaves the gateway: admin views expose
// OAuthView only. The whole record is sealed as one value, so client secrets
// and tokens get the same encryption at rest as MCP headers.
type oauthRecord struct {
	// ServerURL is the upstream URL the tokens were issued for; a server
	// whose URL changed afterwards no longer uses them.
	ServerURL string `json:"server_url"`
	// Resource is the RFC 8707 resource indicator sent on token requests.
	Resource string `json:"resource"`
	Issuer   string `json:"issuer"`
	TokenURL string `json:"token_url"`
	// RedirectURI is the callback the client was registered and authorized
	// with; a stored client is only reused for the same issuer and callback.
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	// AuthStyle is the oauth2.AuthStyle for token requests.
	AuthStyle int `json:"auth_style"`
	// Registered marks a client obtained by dynamic client registration.
	Registered   bool      `json:"registered,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry"`
	SignedInAt   time.Time `json:"signed_in_at"`
	// UpdatedAt changes on every token refresh, so an instance can tell a
	// newer stored token from its own.
	UpdatedAt time.Time `json:"updated_at"`
}

// oauthRowStore persists sealed OAuth records by server name. Both store
// backends implement it.
type oauthRowStore interface {
	listOAuth(ctx context.Context) (map[string]string, error)
	getOAuth(ctx context.Context, name string) (string, error)
	putOAuth(ctx context.Context, name, data string) error
	deleteOAuth(ctx context.Context, name string) error
	swapOAuth(ctx context.Context, name, current, next string) (bool, error)
}

// oauthStore seals records on the way in and opens them on the way out. A
// nil box stores them in plaintext, like MCP headers without
// GOMODEL_ENCRYPTION_KEY.
type oauthStore struct {
	rows oauthRowStore
	box  *encryption.Box
}

func newOAuthStore(store Store, box *encryption.Box) *oauthStore {
	rows, ok := store.(oauthRowStore)
	if !ok {
		return nil
	}
	return &oauthStore{rows: rows, box: box}
}

func (s *oauthStore) seal(name string, record oauthRecord) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode mcp oauth record: %w", err)
	}
	value := string(data)
	if err := s.box.SealFields(oauthSecretKind, name, encryption.Field{Name: oauthSecretField, Value: &value}); err != nil {
		return "", err
	}
	return value, nil
}

func (s *oauthStore) open(name, value string) (oauthRecord, error) {
	if err := s.box.OpenFields(oauthSecretKind, name, encryption.Field{Name: oauthSecretField, Value: &value}); err != nil {
		return oauthRecord{}, err
	}
	var record oauthRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		// The decoder error can quote the input, which holds tokens.
		return oauthRecord{}, fmt.Errorf("decode mcp oauth record for %q: invalid JSON", name)
	}
	return record, nil
}

// list returns every record by server name. A record that cannot be opened
// is skipped and reported in the joined error, so one bad row does not sign
// every other server out.
func (s *oauthStore) list(ctx context.Context) (map[string]oauthRecord, error) {
	rows, err := s.rows.listOAuth(ctx)
	if err != nil {
		return nil, err
	}
	records := make(map[string]oauthRecord, len(rows))
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		record, err := s.open(name, rows[name])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		records[name] = record
	}
	return records, errors.Join(errs...)
}

func (s *oauthStore) get(ctx context.Context, name string) (*oauthRecord, error) {
	value, err := s.rows.getOAuth(ctx, name)
	if err != nil {
		return nil, err
	}
	record, err := s.open(name, value)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// put stores a new sign-in, replacing any earlier one. An error wrapping
// encryption.ErrSealUnconfirmed means the record was written.
func (s *oauthStore) put(ctx context.Context, name string, record oauthRecord) error {
	value, err := s.seal(name, record)
	if err != nil {
		return err
	}
	if err := s.rows.putOAuth(ctx, name, value); err != nil {
		return err
	}
	return s.confirmKey(ctx, name, record, value)
}

// update replaces the stored record with record only while the stored one
// still satisfies same, so a refresh from an old sign-in never undoes a
// sign-out or a newer sign-in. It reports whether it wrote.
func (s *oauthStore) update(ctx context.Context, name string, record oauthRecord, same func(stored oauthRecord) bool) (bool, error) {
	current, err := s.rows.getOAuth(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stored, err := s.open(name, current)
	if err != nil {
		return false, err
	}
	if !same(stored) {
		return false, nil
	}
	next, err := s.seal(name, record)
	if err != nil {
		return false, err
	}
	swapped, err := s.rows.swapOAuth(ctx, name, current, next)
	if err != nil || !swapped {
		return false, err
	}
	return true, s.confirmKey(ctx, name, record, next)
}

// confirmKey re-seals a just-written record when a data key rotation
// activated a new key while the save was in flight (see Box.ConfirmSeal).
// The swap is conditional, so a newer save is never overwritten.
func (s *oauthStore) confirmKey(ctx context.Context, name string, record oauthRecord, written string) error {
	return s.box.ConfirmSeal(func() error {
		resealed, err := s.seal(name, record)
		if err != nil {
			return err
		}
		_, err = s.rows.swapOAuth(ctx, name, written, resealed)
		return err
	}, encryption.Field{Name: oauthSecretField, Value: &written})
}

func (s *oauthStore) delete(ctx context.Context, name string) error {
	return s.rows.deleteOAuth(ctx, name)
}

// reencrypt re-seals every record stored in plaintext or under a data key
// other than the active one. Writes are conditional, so a refresh that
// landed meanwhile is retried rather than overwritten.
func (s *oauthStore) reencrypt(ctx context.Context) (encryption.Report, error) {
	const entity = "mcp_server_oauth"
	rows, err := s.rows.listOAuth(ctx)
	if err != nil {
		return encryption.Report{Entity: entity}, err
	}
	return encryption.ReencryptRows(entity, slices.Sorted(maps.Keys(rows)), func(name string) (encryption.RowOutcome, error) {
		current, err := s.rows.getOAuth(ctx, name)
		if errors.Is(err, ErrNotFound) {
			return encryption.RowUnchanged, nil
		}
		if err != nil {
			return 0, err
		}
		if !s.box.NeedsReseal(encryption.Field{Name: oauthSecretField, Value: &current}) {
			return encryption.RowUnchanged, nil
		}
		record, err := s.open(name, current)
		if err != nil {
			return 0, err
		}
		next, err := s.seal(name, record)
		if err != nil {
			return 0, err
		}
		swapped, err := s.rows.swapOAuth(ctx, name, current, next)
		if err != nil {
			return 0, err
		}
		if !swapped {
			return encryption.RowConflict, nil
		}
		return encryption.RowRewritten, nil
	})
}
