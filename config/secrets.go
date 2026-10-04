package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// SecretResolver resolves the reference part of ${scheme:reference}.
// Implementations must be safe for concurrent use and must never include the
// resolved value in an error.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, reference string) (string, error)
}

// SecretResolverFunc adapts a function to SecretResolver.
type SecretResolverFunc func(ctx context.Context, reference string) (string, error)

// ResolveSecret calls f(ctx, reference).
func (f SecretResolverFunc) ResolveSecret(ctx context.Context, reference string) (string, error) {
	return f(ctx, reference)
}

// ErrUnknownSecretScheme reports a reference whose scheme no resolver is
// registered for.
var ErrUnknownSecretScheme = errors.New("unknown secret scheme")

// SecretError reports a secret reference that could not be resolved. It names
// where the reference was found and its scheme, never the value.
type SecretError struct {
	// Field is the configuration path or environment variable holding the
	// reference, for example "providers.openai.api_key". It may be empty.
	Field  string
	Scheme string
	Err    error
}

func (e *SecretError) Error() string {
	msg := fmt.Sprintf("secret reference ${%s:...}: %v", e.Scheme, e.Err)
	if e.Field == "" {
		return msg
	}
	return e.Field + ": " + msg
}

func (e *SecretError) Unwrap() error { return e.Err }

// schemeHints tells an operator where a scheme that is not registered usually
// comes from.
var schemeHints = map[string]string{
	"vault": "the vault scheme is provided by GoModel Pro vaults (extensions.vaults)",
}

// builtinSecretResolvers are available in every Secrets and cannot be replaced.
var builtinSecretResolvers = map[string]SecretResolver{
	"env":  SecretResolverFunc(resolveEnvSecret),
	"file": SecretResolverFunc(resolveFileSecret),
}

// Secrets resolves ${scheme:reference} secret references for one configuration
// generation. The env and file schemes are built in; extensions add more with
// Register. A nil *Secrets resolves the built-in schemes only. Safe for
// concurrent use.
type Secrets struct {
	mu        sync.RWMutex
	resolvers map[string]SecretResolver
	writer    SecretWriter

	rotation rotation
}

// NewSecrets returns a Secrets with only the built-in schemes registered.
func NewSecrets() *Secrets {
	return &Secrets{resolvers: make(map[string]SecretResolver)}
}

// Register adds a resolver for scheme. The built-in env and file schemes are
// reserved, and a scheme can be registered once.
func (s *Secrets) Register(scheme string, r SecretResolver) error {
	if s == nil {
		return errors.New("register secret scheme: nil Secrets")
	}
	if !validSecretScheme(scheme) {
		return fmt.Errorf("register secret scheme %q: must match [a-z][a-z0-9+.-]*", scheme)
	}
	if r == nil {
		return fmt.Errorf("register secret scheme %q: nil resolver", scheme)
	}
	if _, builtin := builtinSecretResolvers[scheme]; builtin {
		return fmt.Errorf("register secret scheme %q: reserved for the built-in resolver", scheme)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.resolvers[scheme]; dup {
		return fmt.Errorf("register secret scheme %q: already registered", scheme)
	}
	if s.resolvers == nil {
		s.resolvers = make(map[string]SecretResolver)
	}
	s.resolvers[scheme] = r
	return nil
}

// HasReference reports whether value contains at least one secret reference.
// An escaped $${...} is not a reference.
func (s *Secrets) HasReference(value string) bool {
	return hasSecretReference(value)
}

// Resolve replaces every secret reference in value with its resolved value
// and turns each $${ escape into a literal ${. Other text, including a legacy
// ${VAR} left by environment expansion, is kept as is. Resolved values are
// never scanned again.
func (s *Secrets) Resolve(ctx context.Context, value string) (string, error) {
	return s.resolveField(ctx, "", value)
}

// resolveField is Resolve for a value found at field. Every resolution goes
// through here, so a field that held a reference is recorded for rotation
// (see Recheck). An empty field is not recorded.
func (s *Secrets) resolveField(ctx context.Context, field, value string) (string, error) {
	resolved, referenced, err := s.resolveValue(ctx, field, value)
	if err != nil {
		return "", err
	}
	if referenced {
		s.record(field, value, resolved)
	}
	return resolved, nil
}

// resolveValue resolves value without recording it, reporting whether it held
// at least one reference.
func (s *Secrets) resolveValue(ctx context.Context, field, value string) (string, bool, error) {
	if !strings.Contains(value, "${") {
		return value, false, nil
	}
	ctx = withSecretField(ctx, field)
	referenced := false
	var b strings.Builder
	b.Grow(len(value))
	for rest := value; rest != ""; {
		i := strings.Index(rest, "${")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		if i > 0 && rest[i-1] == '$' {
			// $${ is the escape for a literal ${.
			b.WriteString(rest[:i-1])
			b.WriteString("${")
			rest = rest[i+2:]
			continue
		}
		b.WriteString(rest[:i])
		end := strings.IndexByte(rest[i:], '}')
		if end < 0 {
			b.WriteString(rest[i:])
			break
		}
		token := rest[i : i+end+1]
		rest = rest[i+end+1:]
		scheme, reference, ok := parseSecretReference(token[2 : len(token)-1])
		if !ok {
			b.WriteString(token)
			continue
		}
		resolved, err := s.resolveReference(ctx, scheme, reference)
		if err != nil {
			return "", false, &SecretError{Field: field, Scheme: scheme, Err: err}
		}
		referenced = true
		b.WriteString(resolved)
	}
	return b.String(), referenced, nil
}

func (s *Secrets) resolveReference(ctx context.Context, scheme, reference string) (string, error) {
	r, ok := builtinSecretResolvers[scheme]
	if !ok && s != nil {
		s.mu.RLock()
		r, ok = s.resolvers[scheme]
		s.mu.RUnlock()
	}
	if !ok {
		if hint, found := schemeHints[scheme]; found {
			return "", fmt.Errorf("%w %q: %s", ErrUnknownSecretScheme, scheme, hint)
		}
		return "", fmt.Errorf("%w %q: the built-in schemes are env and file; other schemes are registered by extensions", ErrUnknownSecretScheme, scheme)
	}
	return r.ResolveSecret(ctx, reference)
}

type secretFieldKey struct{}

// SecretFieldFromContext returns the field a SecretResolver is resolving
// for, as passed to ResolveSecret: a configuration path such as
// "providers.openai.api_key" or "extensions.vaults.token", or the name of a
// provider environment variable such as "OPENAI_API_KEY". Field paths are not
// secret, so resolvers may log them for audit. The boolean is false for an
// anonymous Resolve.
func SecretFieldFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	field, ok := ctx.Value(secretFieldKey{}).(string)
	return field, ok
}

// withSecretField returns the context resolvers are called with.
func withSecretField(ctx context.Context, field string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if field == "" {
		return ctx
	}
	return context.WithValue(ctx, secretFieldKey{}, field)
}

// hasSecretReference reports whether value holds a reference, using the same
// scan as resolveField.
func hasSecretReference(value string) bool {
	for rest := value; ; {
		i := strings.Index(rest, "${")
		if i < 0 {
			return false
		}
		if i > 0 && rest[i-1] == '$' {
			rest = rest[i+2:]
			continue
		}
		end := strings.IndexByte(rest[i:], '}')
		if end < 0 {
			return false
		}
		if _, _, ok := parseSecretReference(rest[i+2 : i+end]); ok {
			return true
		}
		rest = rest[i+end+1:]
	}
}

// parseSecretReference splits the inside of ${...} into scheme and reference.
// The reference must be non-empty and must not start with "-", which keeps
// the legacy ${VAR:-default} form unambiguous.
func parseSecretReference(inner string) (scheme, reference string, ok bool) {
	scheme, reference, found := strings.Cut(inner, ":")
	if !found || !validSecretScheme(scheme) {
		return "", "", false
	}
	if reference == "" || reference[0] == '-' || strings.ContainsAny(reference, "{}") {
		return "", "", false
	}
	return scheme, reference, true
}

// validSecretScheme reports whether scheme matches [a-z][a-z0-9+.-]*.
func validSecretScheme(scheme string) bool {
	if scheme == "" || scheme[0] < 'a' || scheme[0] > 'z' {
		return false
	}
	for i := 1; i < len(scheme); i++ {
		c := scheme[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
