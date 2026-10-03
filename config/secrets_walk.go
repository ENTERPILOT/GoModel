package config

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ResolveSecrets resolves every secret reference in Config and RawProviders,
// failing on the first one that cannot be resolved. Extensions are skipped:
// DecodeExtension resolves a section when it is decoded. It runs once per
// generation, after the distribution's configuration hook has registered its
// schemes; later calls do nothing, so a resolved value is never scanned again.
func (r *LoadResult) ResolveSecrets(ctx context.Context) error {
	if r == nil || r.secretsResolved {
		return nil
	}
	w := secretWalker{ctx: ctx, secrets: r.Secrets}
	if r.Config != nil {
		if err := w.walk("", reflect.ValueOf(r.Config).Elem()); err != nil {
			return err
		}
	}
	if err := w.walk("providers", reflect.ValueOf(r.RawProviders)); err != nil {
		return err
	}
	r.secretsResolved = true
	return nil
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
		for i := 0; i < v.Len(); i++ {
			if err := w.walk(path+"["+strconv.Itoa(i)+"]", v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Elem() == yamlNodeType {
			return nil // extension sections, resolved by DecodeExtension
		}
		for _, key := range v.MapKeys() {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(v.MapIndex(key))
			if err := w.walk(joinSecretPath(path, mapKeyString(key)), elem); err != nil {
				return err
			}
			v.SetMapIndex(key, elem)
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
