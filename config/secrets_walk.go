package config

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ResolveSecrets resolves every secret reference in Config, failing on the
// first one that cannot be resolved, then re-checks the settings whose
// load-time validation had to accept a reference. Extensions are skipped:
// DecodeExtension resolves a section when it is decoded. RawProviders are
// skipped too: providers.Init resolves them after the provider environment
// variables are merged, so a value an environment variable replaces is never
// looked up.
//
// It runs once per generation, after the distribution's configuration hook
// has registered its schemes. Later calls return the first call's result, so a
// resolved value is never scanned again, not even after a failure left the
// configuration partly resolved.
func (r *LoadResult) ResolveSecrets(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if !r.secretsResolved {
		r.secretsResolved = true
		r.secretsErr = r.resolveSecrets(ctx)
	}
	return r.secretsErr
}

func (r *LoadResult) resolveSecrets(ctx context.Context) error {
	if r.Config == nil {
		return nil
	}
	if err := r.Secrets.ResolveFields(ctx, "", r.Config); err != nil {
		return err
	}
	return validateResolvedMCPServers(r.Config.MCP.Servers)
}

// ResolveFields resolves the secret references in every string reachable from
// target, a pointer or a map: struct fields, slices, maps, and interface
// values such as map[string]any. yaml.Node values are skipped. Errors name the
// field by its YAML path below path, for example "providers.openai.api_key".
//
// Values are resolved in place, but slices and maps reached through a
// settable value are replaced by resolved copies, so data shared with
// another value is not rewritten. Call it once per value: resolved values
// must not be scanned again.
func (s *Secrets) ResolveFields(ctx context.Context, path string, target any) error {
	v := reflect.ValueOf(target)
	if !v.IsValid() {
		return nil
	}
	return secretWalker{ctx: ctx, secrets: s}.walk(path, v)
}

// secretWalker visits every string reachable from a configuration value.
//
// Reflection is deliberate: Config has well over a hundred string fields,
// spread over maps, slices, and free-form plugin settings. An explicit list
// would silently miss the next credential field added, and a reference left
// in place would be sent upstream as a literal credential.
type secretWalker struct {
	ctx     context.Context
	secrets *Secrets
}

var yamlNodeType = reflect.TypeFor[yaml.Node]()

func (w secretWalker) walk(path string, v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		if !strings.Contains(v.String(), "${") {
			return nil
		}
		resolved, err := w.secrets.resolveField(w.ctx, path, v.String())
		if err != nil {
			return err
		}
		v.SetString(resolved)
	case reflect.Pointer:
		if !v.IsNil() {
			return w.walk(path, v.Elem())
		}
	case reflect.Interface:
		// The dynamic value of an interface is not settable, so resolve a copy
		// and store it back.
		if v.IsNil() {
			return nil
		}
		elem := reflect.New(v.Elem().Type()).Elem()
		elem.Set(v.Elem())
		if err := w.walk(path, elem); err != nil {
			return err
		}
		v.Set(elem)
	case reflect.Struct:
		return w.walkStruct(path, v)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Len() > 0 && v.CanSet() {
			clone := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			reflect.Copy(clone, v)
			v.Set(clone)
		}
		for i := 0; i < v.Len(); i++ {
			if err := w.walk(path+"["+strconv.Itoa(i)+"]", v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Elem() == yamlNodeType {
			return nil // extension sections, resolved by DecodeExtension
		}
		target, copied := v, v.CanSet() && !v.IsNil()
		if copied {
			target = reflect.MakeMapWithSize(v.Type(), v.Len())
		}
		for _, key := range v.MapKeys() {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(v.MapIndex(key))
			if err := w.walk(joinSecretPath(path, mapKeyString(key)), elem); err != nil {
				return err
			}
			target.SetMapIndex(key, elem)
		}
		if copied {
			v.Set(target)
		}
	}
	return nil
}

func (w secretWalker) walkStruct(path string, v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() || field.Type == yamlNodeType {
			continue
		}
		name, inline := yamlFieldName(field)
		fieldPath := path
		if !inline {
			fieldPath = joinSecretPath(path, name)
		}
		if err := w.walk(fieldPath, v.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

// yamlFieldName returns the YAML key a field is configured under, so errors
// name the path an operator wrote.
func yamlFieldName(field reflect.StructField) (name string, inline bool) {
	tag, opts, _ := strings.Cut(field.Tag.Get("yaml"), ",")
	if strings.Contains(opts, "inline") {
		return "", true
	}
	if tag == "" || tag == "-" {
		return field.Name, false
	}
	return tag, false
}

func mapKeyString(key reflect.Value) string {
	if key.Kind() == reflect.String {
		return key.String()
	}
	return fmt.Sprint(key.Interface())
}

func joinSecretPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// resolveYAMLNode resolves the scalar values (not mapping keys) under node in
// place. path names node for errors.
func (s *Secrets) resolveYAMLNode(ctx context.Context, path string, node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if !strings.Contains(node.Value, "${") {
			return nil
		}
		resolved, err := s.resolveField(ctx, path, node.Value)
		if err != nil {
			return err
		}
		node.Value = resolved
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := s.resolveYAMLNode(ctx, path, child); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := s.resolveYAMLNode(ctx, path+"["+strconv.Itoa(i)+"]", child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if err := s.resolveYAMLNode(ctx, joinSecretPath(path, node.Content[i].Value), node.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// cloneYAMLNode deep-copies node so resolving a decoded section never writes
// resolved values back into the loaded configuration.
func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	if node.Content != nil {
		clone.Content = make([]*yaml.Node, len(node.Content))
		for i, child := range node.Content {
			clone.Content[i] = cloneYAMLNode(child)
		}
	}
	return &clone
}
