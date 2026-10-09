package mcpgateway

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// ServerSecretEntity is the SecretKey.Entity of admin-managed MCP servers and
// the first segment of their secret field paths, for example
// "mcp_servers.github.headers.Authorization" (ADR-0014 §4). Header values
// are the secret fields: admin-managed servers have no stdio environment.
const ServerSecretEntity = "mcp_servers"

func serverSecretEntity(name string) string {
	return ServerSecretEntity + "." + name
}

func headerSecretField(name string) string {
	return "headers." + name
}

// serverDestinations lists the fields of server that decide where it sends
// its headers, against previous, the row it replaces, for
// config.CheckSecretDestinations: only the URL. A new server has none.
func serverDestinations(previous, server *ManagedServer) []config.SecretDestination {
	if previous == nil {
		return nil
	}
	return []config.SecretDestination{{Field: "url", Stored: previous.URL, Saved: server.URL}}
}

// resolveServer converts a stored row into the spec the upstream runs with,
// header references resolved, and returns the resolution to record once the
// spec is applied. Errors are *config.SecretError values naming the header.
func (s *Service) resolveServer(ctx context.Context, row ManagedServer) (ServerSpec, *config.ResolvedEntity, error) {
	spec := row.Spec()
	entity := serverSecretEntity(row.Name)
	fields := make(map[string]string, len(row.Headers))
	for name, value := range row.Headers {
		fields[entity+"."+headerSecretField(name)] = value
	}
	resolved, err := s.secrets.ResolveEntity(ctx, entity, fields)
	if err != nil {
		return ServerSpec{}, nil, err
	}
	for name, value := range row.Headers {
		spec.Headers[name] = resolved.Value(entity + "." + headerSecretField(name))
		if config.HasSecretReference(value) {
			if spec.HeaderReferences == nil {
				spec.HeaderReferences = make(map[string]string)
			}
			spec.HeaderReferences[name] = value
		}
	}
	return spec, resolved, nil
}

// storeServerSecrets writes every literal header value of server through the
// generation's SecretWriter, when one is registered, replacing it with the
// returned reference. It returns the references it created, which the caller
// releases if the save then fails. keep holds the values of the row still
// stored: a writer-owned reference it does not hold is rejected, and if a
// write fails the references already created are released, except those in
// keep, which a writer that reuses a reference may have returned again.
func (s *Service) storeServerSecrets(ctx context.Context, server *ManagedServer, keep []string) ([]string, error) {
	headers := maps.Clone(server.Headers)
	var written []string
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		key := config.SecretKey{Entity: ServerSecretEntity, ID: server.Name, Field: headerSecretField(name)}
		stored, err := s.secrets.StoreSecret(ctx, key, headers[name], keep)
		if err != nil {
			s.releaseSecrets(ctx, server.Name, written, keep)
			return nil, err
		}
		if stored != headers[name] {
			written = append(written, stored)
			headers[name] = stored
		}
	}
	server.Headers = headers
	return written, nil
}

// releaseSecrets deletes the writer-owned references of previous that current
// no longer holds. It is best effort: the change it follows is committed.
func (s *Service) releaseSecrets(ctx context.Context, name string, previous, current []string) {
	if err := s.secrets.ReleaseSecrets(ctx, previous, current); err != nil {
		slog.Warn("failed to delete secrets of an mcp server from the secret store", "server", core.LogText(name), "error", err)
	}
}

func headerValues(server *ManagedServer) []string {
	if server == nil {
		return nil
	}
	return slices.Collect(maps.Values(server.Headers))
}

// RotateSecrets re-resolves the admin-managed servers owning fields, as
// reported by a secret recheck, and redials each one whose resolved headers
// changed (ADR-0014 §5). Every other server keeps its session untouched. A
// server whose references cannot be re-resolved keeps its current headers.
func (s *Service) RotateSecrets(ctx context.Context, fields []string) error {
	return s.reload(ctx, func(name string) bool {
		return s.secrets.EntityOwnsAny(serverSecretEntity(name), fields)
	})
}
