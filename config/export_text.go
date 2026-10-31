package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// config.Load expands environment variables over the whole config file text
// ($NAME, ${NAME}, ${NAME:-default}) before parsing it, then resolves secret
// references field by field, where $${ is the escape for a literal ${. A
// string an export writes must come back unchanged through both passes, and a
// string taken from elsewhere, such as a description saved in the dashboard,
// must not become a reference: written as is, "${env:GOMODEL_MASTER_KEY}"
// would resolve to the master key when the export is loaded. So every string
// holding a "$" is written double-quoted with each "$" escaped as \x24, which
// the first pass does not read, except where it starts a secret reference the
// export means to keep.

// writtenScalar is a config file string as written, before expansion.
type writtenScalar struct {
	value string
	style yaml.Style
}

// expandedScalars indexes by path the strings of a config file that
// environment expansion rewrites, as written. Map keys are indexed expanded,
// as config.Load reads them. It reports false for a file that does not parse
// unexpanded.
func expandedScalars(data []byte) (map[string]writtenScalar, bool) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false
	}
	if len(root.Content) == 0 {
		return nil, true
	}
	index := make(map[string]writtenScalar)
	var visit func(path string, node *yaml.Node)
	visit = func(path string, node *yaml.Node) {
		switch node.Kind {
		case yaml.ScalarNode:
			if expandString(node.Value) != node.Value {
				index[path] = writtenScalar{value: node.Value, style: node.Style}
			}
		case yaml.SequenceNode:
			for i, item := range node.Content {
				visit(path+"["+strconv.Itoa(i)+"]", item)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				visit(joinSecretPath(path, expandString(node.Content[i].Value)), node.Content[i+1])
			}
		}
	}
	visit("", root.Content[0])
	return index, true
}

// escapeReferences returns s with each ${ escaped as $${, so no reference
// in it is resolved.
func escapeReferences(s string) string {
	return strings.ReplaceAll(s, "${", "$${")
}

// quoteText writes s as a double-quoted YAML scalar whose secret references
// config.Load resolves and nothing else: every "$" is escaped but those
// starting a secret reference.
func quoteText(s string) string {
	keep := make(map[int]bool)
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' || s[i+1] != '{' || (i > 0 && s[i-1] == '$') {
			continue
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			break
		}
		if _, _, ok := parseSecretReference(s[i+2 : i+end]); ok {
			keep[i] = true
		}
	}
	return quoteYAML(s, keep)
}

// quoteYAML writes s as a double-quoted YAML scalar in which each "$" is
// escaped as \x24 unless keep holds its byte offset. Characters YAML cannot
// hold as is are escaped too; an invalid UTF-8 byte becomes U+FFFD.
func quoteYAML(s string, keep map[int]bool) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '$' && !keep[i]:
			b.WriteString(`\x24`)
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError && size == 1:
			b.WriteString(`�`)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 || r == 0xfeff || r == 0xfffe || r == 0xffff:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// maxExportNodes bounds the nodes unaliased copies, so an alias bomb in
// config.yaml fails the export rather than exhausting memory.
const maxExportNodes = 1_000_000

// unaliased returns a copy of node in which each alias is replaced by a copy
// of the node it names, without anchors: the encoder writes mapping keys
// sorted, which can put an alias before its anchor. budget counts down the
// nodes copied. An alias inside the node it names fails at once.
func unaliased(node *yaml.Node, budget *int) (*yaml.Node, error) {
	return unaliasedWithin(node, budget, make(map[*yaml.Node]bool))
}

// unaliasedWithin is unaliased for node within ancestors, the nodes being
// copied around it.
func unaliasedWithin(node *yaml.Node, budget *int, ancestors map[*yaml.Node]bool) (*yaml.Node, error) {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	if ancestors[node] {
		return nil, errors.New("a YAML alias refers to a node that contains it")
	}
	if *budget--; *budget < 0 {
		return nil, errors.New("too many nodes once aliases are expanded")
	}
	clone := *node
	clone.Anchor = ""
	if clone.Kind == yaml.ScalarNode && blockUnsafe(clone.Value) {
		clone.Style = yaml.DoubleQuotedStyle
	}
	if node.Content != nil {
		ancestors[node] = true
		defer delete(ancestors, node)
		clone.Content = make([]*yaml.Node, len(node.Content))
		for i, child := range node.Content {
			copied, err := unaliasedWithin(child, budget, ancestors)
			if err != nil {
				return nil, err
			}
			clone.Content[i] = copied
		}
	}
	return &clone, nil
}

// rewriteStrings returns v with each string rewrite replaces replaced,
// reporting whether it replaced any. Structs, slices, maps, and pointers on
// the way to a replaced string are copied, so v is not modified. Map keys are
// kept.
func rewriteStrings(v reflect.Value, rewrite func(string) (string, bool)) (reflect.Value, bool) {
	switch v.Kind() {
	case reflect.String:
		if s, ok := rewrite(v.String()); ok {
			return reflect.ValueOf(s).Convert(v.Type()), true
		}
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			break
		}
		elem, changed := rewriteStrings(v.Elem(), rewrite)
		if !changed {
			break
		}
		if v.Kind() == reflect.Interface {
			out := reflect.New(v.Type()).Elem()
			out.Set(elem)
			return out, true
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(elem)
		return out, true
	case reflect.Struct:
		if v.Type() == yamlNodeType {
			break
		}
		var out reflect.Value
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			field, changed := rewriteStrings(v.Field(i), rewrite)
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.New(v.Type()).Elem()
				out.Set(v)
			}
			out.Field(i).Set(field)
		}
		if out.IsValid() {
			return out, true
		}
	case reflect.Slice:
		var out reflect.Value
		for i := 0; i < v.Len(); i++ {
			item, changed := rewriteStrings(v.Index(i), rewrite)
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
				reflect.Copy(out, v)
			}
			out.Index(i).Set(item)
		}
		if out.IsValid() {
			return out, true
		}
	case reflect.Map:
		var out reflect.Value
		for _, key := range v.MapKeys() {
			item, changed := rewriteStrings(v.MapIndex(key), rewrite)
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.MakeMapWithSize(v.Type(), v.Len())
				for _, k := range v.MapKeys() {
					out.SetMapIndex(k, v.MapIndex(k))
				}
			}
			out.SetMapIndex(key, item)
		}
		if out.IsValid() {
			return out, true
		}
	}
	return v, false
}
