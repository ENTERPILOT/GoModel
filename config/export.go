package config

import (
	"bytes"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// ExportDocument is a configuration to render as config.yaml with
// Secrets.ExportYAML.
type ExportDocument struct {
	// Config holds every setting, resolved. It is read, never modified.
	Config *Config
	// Providers are the providers to declare, keyed by name, resolved.
	Providers map[string]RawProviderConfig
	// SecretFields names further fields whose values are secrets, by path
	// ("guardrails.rules[0].config.api_key"), for settings whose schema this
	// package does not know, such as a guardrail plugin's.
	SecretFields []string
	// Header is written as comment lines above the document.
	Header string
}

// exportRoot lays out an ExportDocument as config.yaml reads it.
type exportRoot struct {
	*Config   `yaml:",inline"`
	Providers map[string]RawProviderConfig `yaml:"providers,omitempty"`
}

// The `export` struct tag marks a field whose value is, or may embed, a
// secret. On a map or slice it applies to every value.
const (
	// exportTagSecret marks a field whose whole value is a secret.
	exportTagSecret = "secret"
	// exportTagURL marks a URL whose password, or query parameter named like
	// a credential, is a secret.
	exportTagURL = "url"
)

type exportClass int

const (
	exportPlain exportClass = iota
	exportSecret
	exportURL
)

// ExportYAML renders doc as a config.yaml that holds no secret, so it can be
// shared, reviewed, and loaded as is. Each string is written as configured
// where it held a secret reference. A secret that did not come from a
// reference is written as one instead: ${env:NAME} for the environment
// variable in environ that holds it, otherwise ${env:NAME} for a variable to
// set, with a comment saying so, which stops startup until it is set rather
// than loading an empty credential.
//
// Secrets are the fields tagged `export:"secret"` or `export:"url"`, the
// fields in doc.SecretFields, and, where no schema is known (extension
// sections and free-form settings), every string under a key that names a
// credential, such as api_key, token, or password. Any other string that
// equals a resolved secret, or is a URL carrying a password, is one too.
func (s *Secrets) ExportYAML(doc ExportDocument, environ []string) ([]byte, error) {
	if doc.Config == nil {
		return nil, fmt.Errorf("export configuration: no config")
	}
	value := exportRoot{Config: doc.Config, Providers: doc.Providers}
	var root yaml.Node
	if err := root.Encode(value); err != nil {
		return nil, fmt.Errorf("export configuration: %w", err)
	}
	e := newExporter(s, environ, doc.SecretFields)
	e.walk("", reflect.ValueOf(value), &root, exportPlain, "")
	root.HeadComment = doc.Header

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("export configuration: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("export configuration: %w", err)
	}
	return buf.Bytes(), nil
}

// exporter rewrites the secrets of one encoded document.
type exporter struct {
	secrets *Secrets
	env     map[string]string
	names   []string // env names, sorted
	extra   map[string]bool
}

func newExporter(secrets *Secrets, environ []string, secretFields []string) *exporter {
	e := &exporter{secrets: secrets, env: make(map[string]string, len(environ)), extra: make(map[string]bool, len(secretFields))}
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if name == "" || value == "" {
			continue
		}
		e.env[name] = value
		e.names = append(e.names, name)
	}
	slices.Sort(e.names)
	for _, field := range secretFields {
		e.extra[field] = true
	}
	return e
}

// walk visits node, encoded from v found at path. class is how the field
// holding v is exported and envName the variable that sets it, if any. A nil
// slice, map, or pointer under a mapping is dropped: it encodes as [], {}, or
// null, which config.yaml does not always read as unset.
func (e *exporter) walk(path string, v reflect.Value, node *yaml.Node, class exportClass, envName string) {
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if !v.IsValid() {
		return
	}
	if v.Type() == yamlNodeType || v.Kind() == reflect.Interface {
		e.walkUntyped(path, node, class == exportSecret)
		return
	}
	switch node.Kind {
	case yaml.ScalarNode:
		if v.Kind() == reflect.String {
			e.scalar(path, node, class, envName)
		}
	case yaml.SequenceNode:
		if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
			return
		}
		for i, item := range node.Content {
			e.walk(path+"["+strconv.Itoa(i)+"]", v.Index(i), item, class, "")
		}
	case yaml.MappingNode:
		kept := node.Content[:0]
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			childPath := joinSecretPath(path, key.Value)
			switch v.Kind() {
			case reflect.Map:
				child := v.MapIndex(reflect.ValueOf(key.Value).Convert(v.Type().Key()))
				if isNil(child) {
					continue
				}
				// A map key is a setting name too: one naming a credential
				// marks its value secret, as in free-form plugin settings.
				childClass := class
				if childClass == exportPlain && credentialKey(key.Value) {
					childClass = exportSecret
				}
				e.walk(childPath, child, value, childClass, "")
			case reflect.Struct:
				field, child, ok := yamlField(v, key.Value)
				if !ok {
					e.walkUntyped(childPath, value, false)
					break
				}
				if isNil(child) {
					continue
				}
				e.walk(childPath, child, value, exportClassOf(field), field.Tag.Get("env"))
			}
			kept = append(kept, key, value)
		}
		node.Content = kept
	}
}

func isNil(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Invalid:
		return true
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// walkUntyped visits a node no Go type describes. Strings under a key that
// names a credential are secrets.
func (e *exporter) walkUntyped(path string, node *yaml.Node, secret bool) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.ShortTag() != "!!str" && !e.extra[path] {
			return
		}
		class := exportPlain
		if secret {
			class = exportSecret
		}
		e.scalar(path, node, class, "")
	case yaml.SequenceNode:
		for i, item := range node.Content {
			e.walkUntyped(path+"["+strconv.Itoa(i)+"]", item, secret)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			e.walkUntyped(joinSecretPath(path, key), node.Content[i+1], secret || credentialKey(key))
		}
	}
}

// scalar rewrites one string. A setting that held a secret reference is
// written as configured; a secret that did not is written as an env reference.
func (e *exporter) scalar(path string, node *yaml.Node, class exportClass, envName string) {
	if e.extra[path] {
		class = exportSecret
	}
	value := node.Value
	if original, ok := e.secrets.configured(path, value); ok {
		value = original
	} else if class == exportPlain && e.leaksSecret(value) {
		class = exportSecret
	}
	if class == exportURL {
		class = exportPlain
		if urlHasLiteralCredential(value) {
			class = exportSecret
		}
	}
	if class == exportPlain || value == "" || onlyReferences(value) {
		setScalar(node, value, value != node.Value)
		return
	}
	if name, ok := e.envHolding(value, envName); ok {
		setScalar(node, "${env:"+name+"}", true)
		return
	}
	// An Authorization value keeps its scheme: only the credential is secret.
	prefix := ""
	if scheme, credential, ok := cutAuthScheme(value); ok {
		if name, ok := e.envHolding(credential, ""); ok {
			setScalar(node, scheme+" ${env:"+name+"}", true)
			return
		}
		prefix = scheme + " "
	}
	if envName == "" {
		envName = suggestedEnvName(path)
	}
	setScalar(node, prefix+"${env:"+envName+"}", true)
	node.LineComment = "# secret not exported: set " + envName
}

// leaksSecret reports whether a value of a setting not known to hold a secret
// is one all the same: the resolved value of a secret reference, or a URL
// carrying a literal password. It catches a credential the schema, tags, and
// key names miss, such as one kept in a plugin setting with an unusual name.
func (e *exporter) leaksSecret(value string) bool {
	if value == "" {
		return false
	}
	if strings.Contains(value, "://") {
		if _, err := url.Parse(replaceReferences(value, referencePlaceholder)); err == nil && urlHasLiteralCredential(value) {
			return true
		}
	}
	return e.secrets.resolvedSecret(value)
}

// envHolding returns the environment variable whose value is value,
// preferring preferred.
func (e *exporter) envHolding(value, preferred string) (string, bool) {
	if preferred != "" && e.env[preferred] == value {
		return preferred, true
	}
	for _, name := range e.names {
		if e.env[name] == value {
			return name, true
		}
	}
	return "", false
}

func setScalar(node *yaml.Node, value string, changed bool) {
	if !changed {
		return
	}
	node.Value = value
	node.Tag = "!!str"
	node.Style = yaml.DoubleQuotedStyle
}

// ConfiguredValue returns the value field held as configured, references and
// all, when it held a secret reference: field is the path or variable name it
// was resolved under ("providers.openai.api_key", "OPENAI_API_KEY_2").
func (s *Secrets) ConfiguredValue(field string) (string, bool) {
	if s == nil || field == "" {
		return "", false
	}
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	use, ok := s.rotation.uses[field]
	return use.original, ok
}

// configured is ConfiguredValue for a field that still holds what the
// reference resolved to, so a value moved to the path since, such as a
// collapsed API key, is not taken for the one configured there.
func (s *Secrets) configured(field, resolved string) (string, bool) {
	if s == nil || field == "" {
		return "", false
	}
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	use, ok := s.rotation.uses[field]
	if !ok || use.fingerprint != fingerprintSecret(resolved) {
		return "", false
	}
	return use.original, true
}

// resolvedSecret reports whether any recorded reference resolved to value.
func (s *Secrets) resolvedSecret(value string) bool {
	if s == nil {
		return false
	}
	fingerprint := fingerprintSecret(value)
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	for _, use := range s.rotation.uses {
		if use.fingerprint == fingerprint {
			return true
		}
	}
	return false
}

// onlyReferences reports whether value is made of secret references alone,
// optionally after an HTTP auth scheme ("Bearer ${env:TOKEN}").
func onlyReferences(value string) bool {
	if OnlySecretReferences(value) {
		return true
	}
	_, rest, ok := cutAuthScheme(value)
	return ok && OnlySecretReferences(rest)
}

// cutAuthScheme splits an Authorization value into its scheme and credential.
func cutAuthScheme(value string) (scheme, credential string, ok bool) {
	scheme, credential, found := strings.Cut(value, " ")
	switch strings.ToLower(scheme) {
	case "bearer", "basic", "token":
		return scheme, credential, found && credential != ""
	}
	return "", "", false
}

// referencePlaceholder stands in for a secret reference while a URL is
// parsed: braces are not valid in a URL.
const referencePlaceholder = "gomodelsecretreference"

// urlHasLiteralCredential reports whether a URL carries a password, or a
// query parameter named like a credential, that is not made of secret
// references alone. A URL that does not parse is assumed to.
func urlHasLiteralCredential(value string) bool {
	literal := func(s string) bool { return strings.ReplaceAll(s, referencePlaceholder, "") != "" }
	replaced := replaceReferences(value, referencePlaceholder)
	if !strings.Contains(replaced, "://") {
		// A PostgreSQL keyword/value connection string
		// ("host=db user=app password=secret").
		for _, match := range dsnPasswordPattern.FindAllStringSubmatch(replaced, -1) {
			if literal(strings.Trim(match[1], "'")) {
				return true
			}
		}
	}
	if !strings.ContainsAny(value, "@?") {
		return false
	}
	u, err := url.Parse(replaced)
	if err != nil {
		return true
	}
	if password, set := u.User.Password(); set && literal(password) {
		return true
	}
	for key, values := range u.Query() {
		if credentialKey(key) && slices.ContainsFunc(values, literal) {
			return true
		}
	}
	return false
}

// dsnPasswordPattern matches the password of a keyword/value connection string.
var dsnPasswordPattern = regexp.MustCompile(`(?i)(?:^|\s)password\s*=\s*('(?:[^'\\]|\\.)*'|\S*)`)

// replaceReferences replaces every secret reference in value with with.
func replaceReferences(value, with string) string {
	var b strings.Builder
	for rest := value; ; {
		i := strings.Index(rest, "${")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := strings.IndexByte(rest[i:], '}')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		if _, _, ok := parseSecretReference(rest[i+2 : i+end]); ok {
			b.WriteString(with)
		} else {
			b.WriteString(rest[i : i+end+1])
		}
		rest = rest[i+end+1:]
	}
}

// credentialWords are the words of a setting name that mark its value as a
// credential where no schema says so.
var credentialWords = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true, "password": true,
	"passwd": true, "credential": true, "credentials": true, "authorization": true,
	"dsn": true,
}

// credentialKey reports whether a setting name names a credential: one of
// its words is in credentialWords. Words are split at "_", "-", ".", and
// where a lower-case letter is followed by an upper-case one.
func credentialKey(key string) bool {
	var word []rune
	credential := false
	flush := func() {
		if credentialWords[strings.ToLower(string(word))] {
			credential = true
		}
		word = word[:0]
	}
	prevLower := false
	for _, r := range key {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
			prevLower = false
			continue
		case unicode.IsUpper(r) && prevLower:
			flush()
		}
		word = append(word, r)
		prevLower = unicode.IsLower(r)
	}
	flush()
	return credential
}

// suggestedEnvName names the variable to set for a secret at path that no
// variable supplies: GOMODEL_ and the path in upper case.
func suggestedEnvName(path string) string {
	var b strings.Builder
	b.WriteString("GOMODEL")
	underscore := false
	for _, r := range strings.ToUpper(path) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if !underscore {
				b.WriteByte('_')
				underscore = true
			}
			b.WriteRune(r)
			continue
		}
		underscore = false
	}
	return b.String()
}

func exportClassOf(field reflect.StructField) exportClass {
	switch field.Tag.Get("export") {
	case exportTagSecret:
		return exportSecret
	case exportTagURL:
		return exportURL
	}
	return exportPlain
}

// yamlField returns the field of struct v configured under key and its
// value, looking into inlined fields.
func yamlField(v reflect.Value, key string) (reflect.StructField, reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, inline := yamlFieldName(field)
		if inline {
			inner := v.Field(i)
			for inner.Kind() == reflect.Pointer && !inner.IsNil() {
				inner = inner.Elem()
			}
			if inner.Kind() != reflect.Struct {
				continue
			}
			if found, value, ok := yamlField(inner, key); ok {
				return found, value, true
			}
			continue
		}
		if name == key {
			return field, v.Field(i), true
		}
	}
	return reflect.StructField{}, reflect.Value{}, false
}
