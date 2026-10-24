package config

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"maps"
	"slices"
	"sync"
)

// secretFingerprintKey keys every fingerprint in this process. It is random
// per process, so a fingerprint cannot be compared with one taken elsewhere
// or brute-forced offline from a memory dump without the key.
var secretFingerprintKey = sync.OnceValue(func() []byte {
	key := make([]byte, sha256.Size)
	_, _ = rand.Read(key) // never fails; see crypto/rand.Read
	return key
})

type secretFingerprint [sha256.Size]byte

func fingerprintSecret(value string) secretFingerprint {
	mac := hmac.New(sha256.New, secretFingerprintKey())
	_, _ = mac.Write([]byte(value))
	var fp secretFingerprint
	copy(fp[:], mac.Sum(nil))
	return fp
}

// secretUse remembers one field that held a secret reference: the value as
// configured, references and all, and a fingerprint of what it resolved to.
// The resolved value itself is never kept.
type secretUse struct {
	original    string
	fingerprint secretFingerprint
}

// rotation is the bookkeeping that lets a Secrets notice rotated values.
type rotation struct {
	mu   sync.Mutex
	uses map[string]secretUse
	// owned lists the fields each dashboard-managed entity recorded through
	// ResolvedEntity.Record, so a rebuild or delete drops exactly those.
	owned    map[string][]string
	notifier *SecretNotifier
}

// SecretNotifier carries change notifications from every generation's Secrets
// to whichever generation is serving. Notifications coalesce: any number of
// them before the serving generation receives is one pending check.
//
// The process creates one and hands it to each generation's Secrets with
// SetNotifier, so an extension that keeps notifying the Secrets it last saw
// still reaches the serving generation when a reload of a newer one was
// abandoned. A Secrets without a notifier has a private one.
type SecretNotifier struct {
	ch chan struct{}
}

// NewSecretNotifier returns a notifier with no check pending.
func NewSecretNotifier() *SecretNotifier {
	return &SecretNotifier{ch: make(chan struct{}, 1)}
}

// Notify records a pending check without blocking.
func (n *SecretNotifier) Notify() {
	select {
	case n.ch <- struct{}{}:
	default: // a check is already pending
	}
}

// C returns the channel a pending check is delivered on. Only the serving
// generation should receive from it.
func (n *SecretNotifier) C() <-chan struct{} {
	return n.ch
}

func (s *Secrets) record(field, original, resolved string) {
	if s == nil || field == "" {
		return
	}
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	if s.rotation.uses == nil {
		s.rotation.uses = make(map[string]secretUse)
	}
	s.rotation.uses[field] = secretUse{original: original, fingerprint: fingerprintSecret(resolved)}
}

// notifier returns the Secrets' notifier, creating a private one on first use.
func (s *Secrets) notifier() *SecretNotifier {
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	if s.rotation.notifier == nil {
		s.rotation.notifier = NewSecretNotifier()
	}
	return s.rotation.notifier
}

// SetNotifier routes NotifyChanged to n, which the process shares across
// generations. Call it before the generation starts watching Changes.
func (s *Secrets) SetNotifier(n *SecretNotifier) {
	if s == nil || n == nil {
		return
	}
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	s.rotation.notifier = n
}

// NotifyChanged tells core that a secret backend may hold new values. It
// never blocks, and calls that arrive before core gets round to checking are
// coalesced into one check. Core then re-resolves every recorded reference:
// when only provider API keys changed they are swapped in place, any other
// change reloads the configuration as SIGHUP would.
//
// Extensions call it when their backend reports a new version. Each
// configuration generation has its own Secrets, but the gateway shares one
// SecretNotifier between them, so calling it on any generation's Secrets
// reaches the generation that is serving, including after a reload was
// rejected. The serving generation re-checks with its own Secrets, the one
// whose resolvers it was built with. Safe on a nil *Secrets.
func (s *Secrets) NotifyChanged() {
	if s == nil {
		return
	}
	s.notifier().Notify()
}

// Changes returns the channel NotifyChanged signals. Only the serving
// generation receives from it; a nil *Secrets returns a nil channel, which
// never fires.
func (s *Secrets) Changes() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.notifier().C()
}

// SecretRecheck is the outcome of re-resolving every recorded reference. It
// holds the new values of the fields that changed only until it is dropped.
type SecretRecheck struct {
	secrets *Secrets
	changed map[string]recheckedField
}

type recheckedField struct {
	original    string
	value       string
	fingerprint secretFingerprint
}

// Fields returns the paths of the fields whose resolved value changed, sorted.
func (r *SecretRecheck) Fields() []string {
	if r == nil {
		return nil
	}
	fields := make([]string, 0, len(r.changed))
	for field := range r.changed {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	return fields
}

// Value returns the new value of a changed field.
func (r *SecretRecheck) Value(field string) (string, bool) {
	if r == nil {
		return "", false
	}
	f, ok := r.changed[field]
	return f.value, ok
}

// Select returns the part of the recheck whose fields keep reports true.
// Committing it commits only those fields.
func (r *SecretRecheck) Select(keep func(field string) bool) *SecretRecheck {
	if r == nil {
		return nil
	}
	selected := &SecretRecheck{secrets: r.secrets, changed: make(map[string]recheckedField, len(r.changed))}
	for field, f := range r.changed {
		if keep(field) {
			selected.changed[field] = f
		}
	}
	return selected
}

// Commit records the new values as the ones in use, so the next Recheck
// compares against them. Call it once the change has been applied; a change
// that was not applied stays pending and is reported again.
func (r *SecretRecheck) Commit() {
	if r == nil || r.secrets == nil || len(r.changed) == 0 {
		return
	}
	rot := &r.secrets.rotation
	rot.mu.Lock()
	defer rot.mu.Unlock()
	for field, f := range r.changed {
		// A field resolved again since the recheck started already holds a
		// newer record.
		if use, ok := rot.uses[field]; ok && use.original == f.original {
			rot.uses[field] = secretUse{original: f.original, fingerprint: f.fingerprint}
		}
	}
}

// Recheck re-resolves every recorded reference with the resolvers registered
// now and reports the fields whose value changed. It changes nothing: Commit
// does that once the caller has applied the change. Every resolution error is
// returned, joined, as *SecretError values naming field and scheme.
func (s *Secrets) Recheck(ctx context.Context) (*SecretRecheck, error) {
	recheck := &SecretRecheck{secrets: s, changed: make(map[string]recheckedField)}
	if s == nil {
		return recheck, nil
	}
	s.rotation.mu.Lock()
	uses := maps.Clone(s.rotation.uses)
	s.rotation.mu.Unlock()

	// Resolvers may be slow, so they run without the lock held.
	var errs []error
	for _, field := range slices.Sorted(maps.Keys(uses)) {
		use := uses[field]
		value, _, err := s.resolveValue(ctx, field, use.original)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if fp := fingerprintSecret(value); fp != use.fingerprint {
			recheck.changed[field] = recheckedField{original: use.original, value: value, fingerprint: fp}
		}
	}
	return recheck, errors.Join(errs...)
}
