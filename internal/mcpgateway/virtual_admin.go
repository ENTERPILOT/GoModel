package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidVirtualServer marks a virtual server the admin API refuses:
// invalid fields, unknown or nested members, or a name a server holds.
var ErrInvalidVirtualServer = errors.New("invalid virtual MCP server")

type invalidVirtualServerError struct{ msg string }

func (e invalidVirtualServerError) Error() string { return e.msg }

func (e invalidVirtualServerError) Is(target error) bool { return target == ErrInvalidVirtualServer }

func invalidVirtual(format string, args ...any) error {
	return invalidVirtualServerError{msg: fmt.Sprintf(format, args...)}
}

// UpsertVirtual validates and persists one admin-managed virtual server, then
// reconciles. Members must be existing servers. A new virtual server may not
// take a server's slug; one stored before the server appeared stays editable,
// and its edit fails rather than recreating it if a delete wins the race.
// It returns the saved definition's admin view.
func (s *Service) UpsertVirtual(ctx context.Context, virtual ManagedVirtualServer) (VirtualServerView, error) {
	if s.virtualStore == nil {
		return VirtualServerView{}, fmt.Errorf("mcp virtual server persistence is unavailable")
	}
	if err := virtual.Validate(); err != nil {
		return VirtualServerView{}, invalidVirtual("%s", err.Error())
	}
	if s.IsManagedVirtual(virtual.Name) {
		return VirtualServerView{}, invalidVirtual("virtual MCP server %q is managed by config/env and is read-only", virtual.Name)
	}
	for _, member := range virtual.Servers {
		if s.IsVirtual(member) {
			return VirtualServerView{}, invalidVirtual("member %q is a virtual server; virtual servers can only include MCP servers", member)
		}
		if _, ok := s.manager.get(member); !ok {
			return VirtualServerView{}, invalidVirtual("member %q matches no MCP server", member)
		}
	}

	if err := s.persistVirtual(ctx, virtual); err != nil {
		return VirtualServerView{}, err
	}
	if err := s.Reload(ctx); err != nil {
		return VirtualServerView{}, fmt.Errorf("mcp virtual server %q was saved but not applied: %w", virtual.Name, err)
	}
	return s.virtualView(virtual.Spec()), nil
}

// persistVirtual stores virtual unless an MCP server holds its name. It holds
// mutateMu, which server saves hold too, and checks stored rows rather than
// the loaded lists, so neither an overlapping save on this instance nor one
// another instance made before this one refreshed can claim the same name.
func (s *Service) persistVirtual(ctx context.Context, virtual ManagedVirtualServer) error {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()

	taken, err := s.serverNameTaken(ctx, virtual.Name)
	if err != nil {
		return err
	}
	write := s.virtualStore.UpsertVirtual
	if taken {
		// Only an existing row stays editable; Update fails rather than
		// recreating it if a delete wins the race.
		write = s.virtualStore.UpdateVirtual
	}
	if err := write(ctx, virtual); err != nil {
		if errors.Is(err, ErrNotFound) {
			return invalidVirtual("name %q is used by MCP server %q; choose another name", virtual.Name, virtual.Name)
		}
		return err
	}
	return nil
}

// serverNameTaken reports whether a config-declared or stored MCP server
// holds name. It reads the store, so a server another instance saved counts
// before this instance reloads it.
func (s *Service) serverNameTaken(ctx context.Context, name string) (bool, error) {
	if s.IsManaged(name) {
		return true, nil
	}
	if s.store == nil {
		return false, nil
	}
	return storedRowExists(s.store.Get(ctx, name))
}

// virtualNameTaken is serverNameTaken for virtual servers.
func (s *Service) virtualNameTaken(ctx context.Context, name string) (bool, error) {
	if s.IsManagedVirtual(name) {
		return true, nil
	}
	if s.virtualStore == nil {
		return false, nil
	}
	return storedRowExists(s.virtualStore.GetVirtual(ctx, name))
}

func storedRowExists[T any](_ *T, err error) (bool, error) {
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// DeleteVirtual removes one admin-managed virtual server, then reconciles.
func (s *Service) DeleteVirtual(ctx context.Context, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if s.virtualStore == nil {
		return fmt.Errorf("mcp virtual server persistence is unavailable")
	}
	if s.IsManagedVirtual(name) {
		return invalidVirtual("virtual MCP server %q is managed by config/env and is read-only", name)
	}
	if err := s.virtualStore.DeleteVirtual(ctx, name); err != nil {
		return err
	}
	if err := s.Reload(ctx); err != nil {
		return fmt.Errorf("mcp virtual server %q was deleted but the running set was not updated: %w", name, err)
	}
	s.closeEndpointSessions(virtualBindingPrefix + name)
	return nil
}
