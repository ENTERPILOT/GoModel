package mcpgateway

import (
	"context"
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

// reencrypt rewrites every server holding a plaintext header or one sealed
// with an older data key.
func (s *sealedStore) reencrypt(ctx context.Context) (encryption.Report, error) {
	report := encryption.Report{Entity: "mcp_servers"}
	raw, err := s.Store.List(ctx)
	if err != nil {
		return report, err
	}
	report.Rows = len(raw)
	for i := range raw {
		server := raw[i]
		if !s.box.NeedsReseal(headerSecretFields(server.Headers)...) {
			continue
		}
		if err := s.open(&server); err != nil {
			return report, err
		}
		if err := s.Upsert(ctx, server); err != nil {
			return report, err
		}
		report.Reencrypted++
	}
	return report, nil
}
