package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// configFile is one file of the YAML layer: the config file, or a file its
// include: key names.
type configFile struct {
	path string
	// data is the file as written, before environment expansion.
	data []byte
	// root is the file's top-level mapping with environment variables
	// expanded, or nil when the file sets nothing.
	root *yaml.Node
}

// readConfigFiles returns the config file at path followed by the files it
// includes, in the order they are listed. Each is checked by itself, so a
// problem is reported against the file that holds it.
func readConfigFiles(path string, data []byte, strict bool) ([]configFile, error) {
	main, include, err := parseConfigFile(path, data, strict)
	if err != nil {
		return nil, err
	}
	included, err := includedFiles(path, include)
	if err != nil {
		return nil, err
	}
	files := []configFile{main}
	for _, includedPath := range included {
		data, err := os.ReadFile(includedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s, included from %s: %w", includedPath, path, err)
		}
		file, _, err := parseConfigFile(includedPath, data, strict)
		if err != nil {
			return nil, err
		}
		// One list in one file keeps the set of files, and their order, readable.
		if file.root != nil && mappingValueIndex(file.root, "include") >= 0 {
			return nil, fmt.Errorf("failed to parse %s: include is supported only in %s, the config file that includes it", includedPath, path)
		}
		files = append(files, file)
	}
	return files, nil
}

// parseConfigFile checks data as a config file and returns it parsed, with the
// value of its include: key.
func parseConfigFile(path string, data []byte, strict bool) (configFile, []string, error) {
	text := expandString(string(data))
	doc, err := decodeConfigFile(path, text, strict)
	if err != nil {
		return configFile{}, nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(text), &document); err != nil {
		return configFile{}, nil, formatYAMLError(path, err)
	}
	file := configFile{path: path, data: data}
	if len(document.Content) > 0 && document.Content[0].Kind == yaml.MappingNode {
		file.root = document.Content[0]
	}
	return file, doc.Include, nil
}

// includedFiles resolves the include: entries of the config file at path to
// the files to read. A relative entry is relative to the config file's
// directory. An entry with a glob pattern names the files it matches, sorted,
// and may match none; any other entry names one file, which must exist. A
// file is read once, at its first mention under any path, and the config file
// itself never.
func includedFiles(path string, include []string) ([]string, error) {
	var seen []os.FileInfo
	if info, err := os.Stat(path); err == nil {
		seen = append(seen, info)
	}
	var files []string
	for _, entry := range include {
		if strings.TrimSpace(entry) == "" {
			return nil, fmt.Errorf("failed to parse %s: include holds an empty entry", path)
		}
		pattern := entry
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(filepath.Dir(path), pattern)
		}
		matches, isPattern := []string{filepath.Clean(pattern)}, strings.ContainsAny(entry, "*?[")
		if isPattern {
			var err error
			if matches, err = filepath.Glob(pattern); err != nil {
				return nil, fmt.Errorf("failed to parse %s: include %q: %w", path, entry, err)
			}
			slices.Sort(matches)
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			switch {
			case err != nil:
				// Reading it reports why: a named file that is missing.
			case isPattern && info.IsDir():
				// A mounted Kubernetes ConfigMap or Secret holds directories
				// beside its files.
				continue
			case slices.ContainsFunc(seen, func(read os.FileInfo) bool { return os.SameFile(read, info) }):
				continue
			default:
				seen = append(seen, info)
			}
			files = append(files, match)
		}
	}
	return files, nil
}

// mergeConfigFiles returns the one mapping files declare together, or nil when
// they set nothing. Mappings merge key by key, so two files can each add
// providers, or one can hold a provider's API key and another the rest of it.
// Anything else — a value or a list — belongs to the one file that sets it: a
// second file setting it is an error, since neither replacing it nor adding to
// it is what both files say. So does a mapping with a YAML anchor or merge
// key, whose keys other mappings share. A key one file leaves empty yields to
// the other.
func mergeConfigFiles(files []configFile) (*yaml.Node, error) {
	// top holds the merged top-level mapping under one key, so the top level
	// of each file merges as any section does.
	top := &yaml.Node{Kind: yaml.MappingNode}
	// owner maps each merged value to the file it came from, when that is
	// not the file of the mapping holding it.
	owner := make(map[*yaml.Node]string)
	for _, file := range files {
		if file.root == nil {
			continue
		}
		src := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode}, file.root}}
		if err := mergeMapping(top, src, "", "", file.path, owner); err != nil {
			return nil, err
		}
	}
	if len(top.Content) == 0 {
		return nil, nil
	}
	return top.Content[1], nil
}

// mergeMapping adds the entries of src, read from srcFile, to dst, read from
// dstFile unless owner says otherwise. path names both for errors.
func mergeMapping(dst, src *yaml.Node, path, dstFile, srcFile string, owner map[*yaml.Node]string) error {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		at := mappingValueIndex(dst, key.Value)
		if at < 0 {
			dst.Content = append(dst.Content, key, value)
			owner[value] = srcFile
			continue
		}
		existing := dst.Content[at]
		existingFile := dstFile
		if file, ok := owner[existing]; ok {
			existingFile = file
		}
		name := joinSecretPath(path, key.Value)
		switch {
		case isNullNode(value):
		case isNullNode(existing):
			dst.Content[at] = value
			owner[value] = srcFile
		case existing.Kind != yaml.MappingNode || value.Kind != yaml.MappingNode:
			return fmt.Errorf("failed to parse %s: line %d: %s is already set in %s (line %d); a setting can be set in only one config file",
				srcFile, value.Line, name, existingFile, existing.Line)
		case sharesKeys(existing) || sharesKeys(value):
			if name == "" {
				name = "the top level"
			}
			return fmt.Errorf("failed to parse %s: line %d: %s is also set in %s (line %d); a section with a YAML anchor or merge key (<<) can be set in only one config file",
				srcFile, value.Line, name, existingFile, existing.Line)
		default:
			if err := mergeMapping(existing, value, name, existingFile, srcFile, owner); err != nil {
				return err
			}
		}
	}
	return nil
}

// sharesKeys reports whether mapping takes keys from another mapping through
// a merge key, or gives its own to others through an anchor. Keys added to it
// from a second file would reach those mappings too, or silently replace the
// keys it takes.
func sharesKeys(mapping *yaml.Node) bool {
	return mapping.Anchor != "" || mappingValueIndex(mapping, "<<") >= 0
}

// mappingValueIndex returns the index in mapping.Content of the value under
// key, or -1.
func mappingValueIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

func isNullNode(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null"
}
