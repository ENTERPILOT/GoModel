package mcpgateway

import (
	"context"
	"errors"
	"strings"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// mcpSecretKind names managed MCP servers in sealed values' AAD.
const mcpSecretKind = "mcp_server"

// sealedStore encrypts header values on the way into the store and decrypts
// them on the way out. Header names stay readable.
type sealedStore struct {
	Store
	box *encryption.Box
}

func sealStore(store Store, box *encryption.Box) Store {
	if box == nil {
		return store
	}
	return &sealedStore{Store: store, box: box}
}

func headerSecretFields(headers map[string]string) []encryption.Field {
	fields := make([]encryption.Field, 0, len(headers))
	for name := range headers {
		value := headers[name]
		fields = append(fields, encryption.Field{Name: "headers." + name, Value: &value})
	}
	return fields
}

// transformHeaders replaces server's header map with a copy whose values went
// through fn.
func transformHeaders(server *ManagedServer, fn func(id string, fields ...encryption.Field) error) error {
	if len(server.Headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(server.Headers))
	for name, value := range server.Headers {
		if err := fn(strings.TrimSpace(server.Name), encryption.Field{Name: "headers." + name, Value: &value}); err != nil {
			return err
		}
		out[name] = value
	}
	server.Headers = out
	return nil
}

func (s *sealedStore) open(server *ManagedServer) error {
	return transformHeaders(server, func(id string, fields ...encryption.Field) error {
		return s.box.OpenFields(mcpSecretKind, id, fields...)
	})
}

func (s *sealedStore) List(ctx context.Context) ([]ManagedServer, error) {
	servers, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if err := s.open(&servers[i]); err != nil {
			return nil, err
		}
	}
	return servers, nil
}

func (s *sealedStore) Get(ctx context.Context, name string) (*ManagedServer, error) {
	server, err := s.Store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.open(server); err != nil {
		return nil, err
	}
	return server, nil
}

// Upsert seals a copy of the headers; the caller's map is left untouched.
func (s *sealedStore) Upsert(ctx context.Context, server ManagedServer) error {
	if err := transformHeaders(&server, func(id string, fields ...encryption.Field) error {
		return s.box.SealFields(mcpSecretKind, id, fields...)
	}); err != nil {
		return err
	}
	return s.Store.Upsert(ctx, server)
}

// headerSwapper replaces a server's headers only while they still hold the
// values read earlier. Both store backends implement it.
type headerSwapper interface {
	swapHeaders(ctx context.Context, current, next ManagedServer) (bool, error)
}

// reencrypt rewrites every server holding a plaintext header or one sealed
// with an older data key. Each write is conditional on the headers still being
// what was read, so a concurrent admin edit is never overwritten: the row is
// read again and retried.
func (s *sealedStore) reencrypt(ctx context.Context, swap headerSwapper) (encryption.Report, error) {
	listed, err := s.Store.List(ctx)
	if err != nil {
		return encryption.Report{Entity: "mcp_servers"}, err
	}
	names := make([]string, len(listed))
	for i, row := range listed {
		names[i] = row.Name
	}
	return encryption.ReencryptRows("mcp_servers", names, func(name string) (encryption.RowOutcome, error) {
		return s.reencryptRow(ctx, swap, name)
	})
}

func (s *sealedStore) reencryptRow(ctx context.Context, swap headerSwapper, name string) (encryption.RowOutcome, error) {
	current, err := s.Store.Get(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return encryption.RowUnchanged, nil
	}
	if err != nil {
		return 0, err
	}
	if !s.box.NeedsReseal(headerSecretFields(current.Headers)...) {
		return encryption.RowUnchanged, nil
	}
	next := *current
	if err := s.open(&next); err != nil {
		return 0, err
	}
	if err := transformHeaders(&next, func(id string, fields ...encryption.Field) error {
		return s.box.SealFields(mcpSecretKind, id, fields...)
	}); err != nil {
		return 0, err
	}
	swapped, err := swap.swapHeaders(ctx, *current, next)
	if err != nil {
		return 0, err
	}
	if !swapped {
		return encryption.RowConflict, nil
	}
	return encryption.RowRewritten, nil
}
