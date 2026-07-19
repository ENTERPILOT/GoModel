package headerpolicy

import (
	"errors"
	"strings"
)

type definitionScanner interface {
	Scan(dest ...any) error
}

type definitionRows interface {
	definitionScanner
	Next() bool
	Err() error
}

func normalizeDefinitionName(name string) string {
	return strings.TrimSpace(name)
}

func collectDefinitions(rows definitionRows, scan func(definitionScanner) (Definition, error)) ([]Definition, error) {
	result := make([]Definition, 0)
	for rows.Next() {
		definition, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func storeNotFound(err, backendNotFound error) error {
	if errors.Is(err, backendNotFound) {
		return ErrNotFound
	}
	return err
}

type persistedDefinition struct {
	Methods []string    `json:"methods,omitempty" bson:"methods,omitempty"`
	Paths   []string    `json:"paths,omitempty" bson:"paths,omitempty"`
	When    []Condition `json:"when,omitempty" bson:"when,omitempty"`
	Actions []Action    `json:"actions" bson:"actions"`
}

func persistedFromDefinition(def Definition) persistedDefinition {
	return persistedDefinition{Methods: def.Methods, Paths: def.Paths, When: def.When, Actions: def.Actions}
}

func definitionFromPersisted(name, description string, persisted persistedDefinition) (Definition, error) {
	return normalizeDefinition(Definition{
		Name: name, Description: description, Methods: persisted.Methods,
		Paths: persisted.Paths, When: persisted.When, Actions: persisted.Actions,
	})
}
