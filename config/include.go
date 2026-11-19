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
// file is read once, at its first mention, and the config file itself never.
func includedFiles(path string, include []string) ([]string, error) {
	seen := map[string]bool{filepath.Clean(path): true}
	var files []string
	for _, entry := range include {
		pattern := strings.TrimSpace(entry)
		if pattern == "" {
			return nil, fmt.Errorf("failed to parse %s: include holds an empty entry", path)
		}
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(filepath.Dir(path), pattern)
		}
		matches := []string{pattern}
		if strings.ContainsAny(pattern, "*?[") {
			var err error
			if matches, err = globFiles(pattern); err != nil {
				return nil, fmt.Errorf("failed to parse %s: include %q: %w", path, entry, err)
			}
		}
		for _, match := range matches {
			if match = filepath.Clean(match); !seen[match] {
				seen[match] = true
				files = append(files, match)
			}
		}
	}
	return files, nil
}

// globFiles returns the files pattern matches, sorted. Directories are left
// out: a mounted Kubernetes ConfigMap or Secret holds some beside its files.
func globFiles(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	matches = slices.DeleteFunc(matches, func(match string) bool {
		info, err := os.Stat(match)
		return err == nil && info.IsDir()
	})
	slices.Sort(matches)
	return matches, nil
}

// mergeConfigFiles returns the one mapping files declare together, or nil when
// they set nothing. Mappings merge key by key, so two files can each add
// providers, or one can hold a provider's API key and another the rest of it.
// Anything else — a value or a list — belongs to the one file that sets it: a
// second file setting it is an error, since neither replacing it nor adding to
// it is what both files say. A key one file leaves empty yields to the other.
func mergeConfigFiles(files []configFile) (*yaml.Node, error) {
	var merged *yaml.Node
	// owner maps each merged value to the file it came from, when that is
	// not the file of the mapping holding it.
	owner := make(map[*yaml.Node]string)
	for _, file := range files {
		switch {
		case file.root == nil:
		case merged == nil:
			merged = file.root
			owner[merged] = file.path
		default:
			if err := mergeMapping(merged, file.root, "", owner[merged], file.path, owner); err != nil {
				return nil, err
			}
		}
	}
	return merged, nil
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
		switch {
		case isNullNode(value):
		case isNullNode(existing):
			dst.Content[at] = value
			owner[value] = srcFile
		case existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode:
			if err := mergeMapping(existing, value, joinSecretPath(path, key.Value), existingFile, srcFile, owner); err != nil {
				return err
			}
		default:
			return fmt.Errorf("failed to parse %s: line %d: %s is already set in %s (line %d); a setting can be set in only one config file",
				srcFile, value.Line, joinSecretPath(path, key.Value), existingFile, existing.Line)
		}
	}
	return nil
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
