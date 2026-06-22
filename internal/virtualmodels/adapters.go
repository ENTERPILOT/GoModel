package virtualmodels

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gomodel/internal/aliases"
	"gomodel/internal/modeloverrides"
	"gomodel/internal/validation"
)

// ensureSourceKind rejects an upsert that would clobber an existing virtual
// model of the other kind. Source is a single namespace, so a redirect and an
// access policy cannot share a name.
//
// Callers hold the shared writeMu, so this check-then-write is atomic within a
// process. Across replicas sharing one database it is best-effort: the guard
// plus the rarity of the trigger (an alias named identically to an override
// selector, written concurrently to two replicas) make the residual race
// acceptable on the admin config plane. A fully atomic guarantee would require
// a per-backend conditional upsert.
func ensureSourceKind(ctx context.Context, store Store, source string, wantRedirect bool) error {
	existing, err := store.Get(ctx, source)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.IsRedirect() == wantRedirect {
		return nil
	}
	other := "an access policy"
	if !wantRedirect {
		other = "an alias"
	}
	return validation.NewError(fmt.Sprintf("source %q is already used by %s", source, other), nil)
}

// deleteOfKind deletes source only when it exists and matches the wanted kind,
// returning the engine's not-found error otherwise.
func deleteOfKind(ctx context.Context, store Store, source string, wantRedirect bool, notFound error) error {
	existing, err := store.Get(ctx, source)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return notFound
		}
		return err
	}
	if existing.IsRedirect() != wantRedirect {
		return notFound
	}
	if err := store.Delete(ctx, source); err != nil {
		if errors.Is(err, ErrNotFound) {
			return notFound
		}
		return err
	}
	return nil
}

// redirectStore exposes the virtual_models store as an aliases.Store, scoped to
// redirect rows. The aliases.Service runs unchanged on top of it. writeMu is
// shared with policyStore so cross-kind check-then-write upserts/deletes
// serialize (the two engines otherwise hold independent locks).
type redirectStore struct {
	store   Store
	writeMu *sync.Mutex
}

func (r redirectStore) List(ctx context.Context) ([]aliases.Alias, error) {
	vms, err := r.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]aliases.Alias, 0, len(vms))
	for _, vm := range vms {
		if vm.IsRedirect() {
			result = append(result, vm.toAlias())
		}
	}
	return result, nil
}

func (r redirectStore) Get(ctx context.Context, name string) (*aliases.Alias, error) {
	vm, err := r.store.Get(ctx, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, aliases.ErrNotFound
		}
		return nil, err
	}
	if !vm.IsRedirect() {
		return nil, aliases.ErrNotFound
	}
	alias := vm.toAlias()
	return &alias, nil
}

func (r redirectStore) Upsert(ctx context.Context, alias aliases.Alias) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := ensureSourceKind(ctx, r.store, alias.Name, true); err != nil {
		return err
	}
	return r.store.Upsert(ctx, vmFromAlias(alias))
}

func (r redirectStore) Delete(ctx context.Context, name string) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return deleteOfKind(ctx, r.store, name, true, aliases.ErrNotFound)
}

// Close is a no-op: the shared store is closed once by the factory.
func (r redirectStore) Close() error { return nil }

// policyStore exposes the virtual_models store as a modeloverrides.Store, scoped
// to policy rows. The modeloverrides.Service runs unchanged on top of it. writeMu
// is shared with redirectStore (see its doc).
type policyStore struct {
	store   Store
	writeMu *sync.Mutex
}

func (p policyStore) List(ctx context.Context) ([]modeloverrides.Override, error) {
	vms, err := p.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]modeloverrides.Override, 0, len(vms))
	for _, vm := range vms {
		if !vm.IsRedirect() {
			result = append(result, vm.toOverride())
		}
	}
	return result, nil
}

func (p policyStore) Upsert(ctx context.Context, override modeloverrides.Override) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := ensureSourceKind(ctx, p.store, override.Selector, false); err != nil {
		return err
	}
	return p.store.Upsert(ctx, vmFromOverride(override))
}

func (p policyStore) Delete(ctx context.Context, selector string) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return deleteOfKind(ctx, p.store, selector, false, modeloverrides.ErrNotFound)
}

// Close is a no-op: the shared store is closed once by the factory.
func (p policyStore) Close() error { return nil }
