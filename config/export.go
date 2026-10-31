package config

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ExportDocument is a configuration to render as config.yaml with
// LoadResult.ExportYAML.
type ExportDocument struct {
	// Config holds every setting, resolved. It is read, never modified.
	Config *Config
	// Providers are the providers to declare, keyed by name, resolved.
	Providers map[string]RawProviderConfig
	// References names the fields, by path ("providers.groq.api_key"), whose
	// values are written as stored, secret references unresolved, such as the
	// API keys of a provider credential saved in the dashboard. Their
	// references are kept; a reference in any other value that was not
	// configured as one is written as text.
	References []string
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
	// exportTagURL marks a URL whose user information, or query parameter
	// named like a credential, is a secret.
	exportTagURL = "url"
	// exportTagArgs marks command-line arguments, in which the value of a
	// flag named like a credential ("--api-key", "GITHUB_TOKEN=") is a secret.
	exportTagArgs = "args"
)

type exportClass int

const (
	exportPlain exportClass = iota
	exportSecret
	exportURL
	exportArgs
)

// ExportYAML renders doc as a config.yaml that holds no secret, so it can be
// shared, reviewed, and loaded as is. A string that held a secret reference
// is written as configured, and one environment expansion rewrote as written
// in config.yaml. A secret that did not come from a reference is written as
// one instead: ${env:NAME} for the environment variable in environ that holds
// it, otherwise ${env:NAME} for a variable to set, with a comment saying so,
// which stops startup until it is set rather than loading an empty credential.
// Any other "$" is escaped, so the document reads back what it shows.
//
// Secrets are the fields tagged `export:"secret"`, `export:"url"`, or
// `export:"args"`, the fields in doc.SecretFields, and every string under a
// map key, or a key of a section no schema describes (extension sections and
// free-form settings), that names a credential, such as api_key, token, or
// password. Any other string that equals a resolved secret, is a URL carrying
// user information, or is a connection string carrying a password is one too.
func (r *LoadResult) ExportYAML(doc ExportDocument, environ []string) ([]byte, error) {
	if r == nil || doc.Config == nil {
		return nil, fmt.Errorf("export configuration: no config")
	}
	// Without the file as written, a value an environment variable supplied
	// through $NAME could be written out in place of $NAME.
	if r.expandedUnknown {
		return nil, fmt.Errorf("export configuration: config.yaml parses only once environment variables are expanded, so the export cannot tell which values they supplied; quote the values that hold them")
	}
	cfg := *doc.Config
	if len(cfg.Extensions) > 0 {
		cfg.Extensions = make(map[string]yaml.Node, len(doc.Config.Extensions))
		budget := maxExportNodes
		for name, node := range doc.Config.Extensions {
			copied, err := unaliased(&node, &budget)
			if err != nil {
				return nil, fmt.Errorf("export configuration: extensions.%s: %w", name, err)
			}
			cfg.Extensions[name] = *copied
		}
	}
	// The encoder writes a multi-line string starting with whitespace in a
	// block style it cannot read back, which fails Node.Encode. Such strings
	// are encoded as placeholders, and set back double-quoted by the walk.
	blocks := make(map[string]string)
	blockToken := "gomodelexportblock" + rand.Text() + "n"
	value, _ := rewriteStrings(reflect.ValueOf(exportRoot{Config: &cfg, Providers: doc.Providers}), func(s string) (string, bool) {
		if !blockUnsafe(s) {
			return "", false
		}
		token := blockToken + strconv.Itoa(len(blocks)) + "z"
		blocks[token] = s
		return token, true
	})
	var surveyed, root yaml.Node
	if err := surveyed.Encode(value.Interface()); err != nil {
		return nil, fmt.Errorf("export configuration: %w", err)
	}
	if err := root.Encode(value.Interface()); err != nil {
		return nil, fmt.Errorf("export configuration: %w", err)
	}
	// A first pass finds the secret settings, so only what resolved into
	// one counts as a secret wherever else it appears.
	survey := newExporter(r.Secrets, environ, doc, r.expanded)
	survey.survey, survey.blocks = true, blocks
	survey.walk("", value, &surveyed, exportPlain, "")
	e := newExporter(r.Secrets, environ, doc, r.expanded)
	e.secretPaths, e.secretValues, e.blocks = survey.secretPaths, survey.secretValues, blocks
	e.walk("", value, &root, exportPlain, "")
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
	return []byte(strings.NewReplacer(e.texts...).Replace(buf.String())), nil
}

// exporter rewrites the secrets of one encoded document.
type exporter struct {
	secrets *Secrets
	env     map[string]string
	names   []string // env names, sorted
	stored  map[string]bool
	extra   map[string]bool
	// unresolved holds the paths of the sections config.Load leaves
	// unresolved, which hold their references as configured: extension
	// sections and settings tagged `secrets:"deferred"`.
	unresolved []string
	expanded   map[string]writtenScalar
	// texts pairs each placeholder written in place of a string with the
	// YAML scalar that replaces it once the document is encoded: the encoder
	// writes "$" as is, so strings holding one are quoted by quoteYAML.
	texts []string
	// token starts every placeholder. It is random, so no value can hold one.
	token string
	// survey, set on a first pass, only collects secretPaths: the paths of
	// the settings holding a secret by their schema or name, and
	// secretValues: the secrets they hold, however they were supplied.
	survey       bool
	secretPaths  map[string]bool
	secretValues map[string]bool
	// suggested maps each variable name suggested for a secret to its path,
	// so two paths never share one.
	suggested map[string]string
	// blocks maps the placeholders strings were encoded as to the strings.
	blocks map[string]string
}

func newExporter(secrets *Secrets, environ []string, doc ExportDocument, expanded map[string]writtenScalar) *exporter {
	e := &exporter{
		secrets:      secrets,
		env:          make(map[string]string, len(environ)),
		stored:       make(map[string]bool, len(doc.References)),
		extra:        make(map[string]bool, len(doc.SecretFields)),
		expanded:     expanded,
		token:        "gomodelexport" + rand.Text() + "n",
		secretPaths:  make(map[string]bool),
		secretValues: make(map[string]bool),
		suggested:    make(map[string]string),
	}
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if name == "" || value == "" {
			continue
		}
		e.env[name] = value
		e.names = append(e.names, name)
	}
	slices.Sort(e.names)
	for _, field := range doc.References {
		e.stored[field] = true
	}
	for _, field := range doc.SecretFields {
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
			e.scalar(path, node, class, envName, 0)
		}
	case yaml.SequenceNode:
		if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
			return
		}
		if class == exportArgs {
			e.args(path, node)
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
				childClass := class
				if class == exportPlain && credentialKey(key.Value) {
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
				if field.Tag.Get("secrets") == secretsDeferred || field.Type == yamlNodeType || field.Type.Kind() == reflect.Map && field.Type.Elem() == yamlNodeType {
					e.unresolved = append(e.unresolved, childPath)
				}
				e.walk(childPath, child, value, exportClassOf(field), field.Tag.Get("env"))
			}
			e.key(key)
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
// names a credential are secrets. Comments, which config.Load expands too,
// are dropped.
func (e *exporter) walkUntyped(path string, node *yaml.Node, secret bool) {
	node.HeadComment, node.LineComment, node.FootComment = "", "", ""
	switch node.Kind {
	case yaml.ScalarNode:
		// A number or a value of another type is a string to the code
		// decoding the section, so under a credential key it is a secret too.
		switch node.ShortTag() {
		case "!!null":
			return
		case "!!int", "!!float", "!!bool":
			if !secret {
				return
			}
		}
		class := exportPlain
		if secret {
			class = exportSecret
		}
		e.scalar(path, node, class, "", 0)
	case yaml.SequenceNode:
		for i, item := range node.Content {
			e.walkUntyped(path+"["+strconv.Itoa(i)+"]", item, secret)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			e.walkUntyped(joinSecretPath(path, key.Value), node.Content[i+1], secret || credentialKey(key.Value))
			key.HeadComment, key.LineComment, key.FootComment = "", "", ""
			e.key(key)
		}
	}
}

// args visits command-line arguments: the value of a flag that names a
// credential is a secret, after "=" or ":" or as the next argument.
func (e *exporter) args(path string, node *yaml.Node) {
	next := false
	for i, item := range node.Content {
		itemPath := path + "[" + strconv.Itoa(i) + "]"
		if next {
			next = false
			e.scalar(itemPath, item, exportSecret, "", 0)
			continue
		}
		prefix, flag := argCredential(item.Value)
		next = flag
		if _, _, ok := cutAuthScheme(item.Value); ok && prefix < 0 {
			e.scalar(itemPath, item, exportSecret, "", 0)
			continue
		}
		if prefix >= 0 {
			e.scalar(itemPath, item, exportSecret, "", prefix)
			continue
		}
		e.scalar(itemPath, item, exportPlain, "", 0)
	}
}

// key writes a mapping key holding a "$" escaped. Keys are not resolved, so
// a reference in one is text.
func (e *exporter) key(node *yaml.Node) {
	if e.survey || !strings.Contains(node.Value, "$") {
		return
	}
	text := quoteYAML(node.Value, nil)
	e.setText(node, text)
	// As long as the key, so the encoder writes a long one as an explicit
	// "? key", which YAML requires past 1024 characters.
	if pad := len(text) - len(node.Value); pad > 0 {
		e.texts[len(e.texts)-2] += strings.Repeat("x", pad)
		node.Value = e.texts[len(e.texts)-2]
	}
}

// scalar rewrites one string, of which the first prefix bytes are not secret
// even when the rest is ("--api-key=").
func (e *exporter) scalar(path string, node *yaml.Node, class exportClass, envName string, prefix int) {
	if original, ok := e.blocks[node.Value]; ok {
		node.Value = original
		setScalar(node, original, true)
	}
	if e.extra[path] {
		class = exportSecret
	}
	// configured: value is text as configured or stored, references
	// unresolved. Restored from a recorded reference, it is not compared with
	// resolved secrets, which hold its own resolution.
	value, configured, restored := node.Value, e.stored[path] || e.isUnresolved(path), false
	if !configured {
		if original, ok := e.secrets.configured(path, value); ok {
			value, configured, restored = original, true, true
		}
	}
	if e.survey {
		if class == exportSecret || class == exportURL && urlHasLiteralCredential(value) {
			e.secretPaths[path] = true
		}
		if secret := node.Value; class == exportSecret && prefix <= len(secret) {
			secret = secret[prefix:]
			e.recordSecret(secret)
			if _, credential, ok := cutAuthScheme(secret); ok {
				e.recordSecret(strings.TrimLeft(credential, " \t"))
			}
		}
		if class == exportURL {
			credentials, _ := urlCredentials(node.Value)
			for _, credential := range credentials {
				e.recordSecret(credential)
			}
		}
		return
	}
	switch class {
	case exportPlain:
		if e.leaksSecret(value, !restored) {
			class = exportSecret
		}
	case exportURL:
		class = exportPlain
		if urlHasLiteralCredential(value) || e.leaksSecret(value, !restored) {
			class = exportSecret
		}
	}
	if class == exportPlain || value == "" {
		e.setValue(path, node, value, configured)
		return
	}
	head, secret := "", value
	if prefix > 0 && prefix <= len(node.Value) && strings.HasPrefix(value, node.Value[:prefix]) {
		head, secret = value[:prefix], value[prefix:]
	}
	if configured && onlyReferences(secret) {
		e.setValue(path, node, value, true)
		return
	}
	e.setSecret(path, node, head, secret, envName)
}

// isUnresolved reports whether path is in a section config.Load leaves
// unresolved.
func (e *exporter) isUnresolved(path string) bool {
	for _, section := range e.unresolved {
		if rest, ok := strings.CutPrefix(path, section); ok && (rest == "" || rest[0] == '.' || rest[0] == '[') {
			return true
		}
	}
	return false
}

// setValue writes a string that is not secret: as written in config.yaml
// when environment expansion rewrote it, otherwise with its secret
// references kept if it was configured or stored with them, and as text if
// not.
func (e *exporter) setValue(path string, node *yaml.Node, value string, configured bool) {
	if written, ok := e.expanded[path]; ok && expandString(written.value) == value {
		node.Value, node.Tag, node.Style = written.value, "!!str", written.style
		return
	}
	if !strings.Contains(value, "$") {
		setScalar(node, value, value != node.Value || blockUnsafe(value))
		return
	}
	if !configured {
		value = escapeReferences(value)
	}
	e.setText(node, quoteText(value))
}

// setSecret writes secret, which follows the text head, as an env reference.
func (e *exporter) setSecret(path string, node *yaml.Node, head, secret, envName string) {
	head = escapeReferences(head)
	if name, ok := e.envHolding(secret, envName); ok {
		e.setText(node, quoteText(head+"${env:"+name+"}"))
		return
	}
	// An Authorization value keeps its scheme: only the credential is secret.
	if scheme, credential, ok := cutAuthScheme(secret); ok {
		if name, ok := e.envHolding(credential, ""); ok {
			e.setText(node, quoteText(head+scheme+" ${env:"+name+"}"))
			return
		}
		head += scheme + " "
	}
	if envName == "" {
		envName = e.suggestEnvName(path)
	}
	e.setText(node, quoteText(head+"${env:"+envName+"}"))
	node.LineComment = "# secret not exported: set " + envName
}

// suggestEnvName returns suggestedEnvName(path), numbered when another path
// was given that name ("a.b" and "a_b").
func (e *exporter) suggestEnvName(path string) string {
	base := suggestedEnvName(path)
	name := base
	for n := 2; e.suggested[name] != "" && e.suggested[name] != path; n++ {
		name = base + "_" + strconv.Itoa(n)
	}
	e.suggested[name] = path
	return name
}

// setText puts a placeholder in node that ExportYAML replaces with text, a
// YAML scalar.
func (e *exporter) setText(node *yaml.Node, text string) {
	token := e.token + strconv.Itoa(len(e.texts)/2) + "z"
	e.texts = append(e.texts, token, text)
	node.Value, node.Tag, node.Style = token, "!!str", 0
}

// blockUnsafe reports whether value is a multi-line string that starts with
// whitespace: the encoder writes it in a block style it cannot read back.
func blockUnsafe(value string) bool {
	return strings.Contains(value, "\n") && value != "" && (value[0] == '\n' || value[0] == ' ' || value[0] == '\t')
}

func setScalar(node *yaml.Node, value string, changed bool) {
	if !changed {
		return
	}
	node.Value = value
	node.Tag = "!!str"
	node.Style = yaml.DoubleQuotedStyle
}

// recordSecret records secret, unless it is too short or a reference, so
// copies of it elsewhere are redacted.
func (e *exporter) recordSecret(secret string) {
	if len(secret) >= minSecretCopy && !onlyReferences(secret) && !strings.Contains(secret, referencePlaceholder) {
		e.secretValues[secret] = true
	}
}

// minSecretCopy is the shortest secret whose copies elsewhere are redacted:
// a shorter one would match ordinary text.
const minSecretCopy = 8

// leaksSecret reports whether a value of a setting not known to hold a secret
// is one all the same: a URL carrying user information, an assignment of a
// credential ("password=..."), a bearer token, text holding a secret
// setting's value, or, when resolved is set, what a reference in a secret
// setting resolved to. It catches a credential the schema, tags, and key
// names miss, such as one kept in a plugin setting with an unusual name.
func (e *exporter) leaksSecret(value string, resolved bool) bool {
	if value == "" {
		return false
	}
	if strings.Contains(value, "://") && urlHasLiteralCredential(value) {
		return true
	}
	if assignsLiteralCredential(value) || bearerCredential(value) {
		return true
	}
	// A copy of a secret setting's value, such as one an environment
	// variable supplied, which no reference records.
	for secret := range e.secretValues {
		if strings.Contains(value, secret) {
			return true
		}
	}
	// Only a value resolved into a secret setting is a secret: a tenant can
	// copy a resolved base URL into a description.
	return resolved && e.secrets.resolvedSecretWhere(value, func(field string) bool {
		return e.secretPaths[field] || !strings.Contains(field, ".")
	})
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

// resolvedSecretWhere reports whether a reference recorded for a field
// include accepts resolved to value.
func (s *Secrets) resolvedSecretWhere(value string, include func(field string) bool) bool {
	if s == nil {
		return false
	}
	fingerprint := fingerprintSecret(value)
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	for field, use := range s.rotation.uses {
		if use.fingerprint == fingerprint && include(field) {
			return true
		}
	}
	return false
}

func exportClassOf(field reflect.StructField) exportClass {
	switch field.Tag.Get("export") {
	case exportTagSecret:
		return exportSecret
	case exportTagURL:
		return exportURL
	case exportTagArgs:
		return exportArgs
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
