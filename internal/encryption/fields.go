package encryption

import "fmt"

// Field points at one secret value of an entity. Name is the field name bound
// into the value's additional authenticated data.
type Field struct {
	Name  string
	Value *string
}

// SealFields seals every field of one entity in place.
func (b *Box) SealFields(kind, id string, fields ...Field) error {
	for _, field := range fields {
		sealed, err := b.Seal(AAD(kind, id, field.Name), *field.Value)
		if err != nil {
			return fmt.Errorf("encrypt %s %q field %s: %w", kind, id, field.Name, err)
		}
		*field.Value = sealed
	}
	return nil
}

// OpenFields opens every field of one entity in place.
func (b *Box) OpenFields(kind, id string, fields ...Field) error {
	for _, field := range fields {
		opened, err := b.Open(AAD(kind, id, field.Name), *field.Value)
		if err != nil {
			return fmt.Errorf("decrypt %s %q field %s: %w", kind, id, field.Name, err)
		}
		*field.Value = opened
	}
	return nil
}

// NeedsReseal reports whether any field holds plaintext or a value sealed
// with a data key other than the active one. It is always false for a
// disabled Box.
func (b *Box) NeedsReseal(fields ...Field) bool {
	if !b.Enabled() {
		return false
	}
	for _, field := range fields {
		if *field.Value != "" && !b.IsCurrent(*field.Value) {
			return true
		}
	}
	return false
}

// Report counts what a re-encryption pass did to one entity kind. It never
// carries values.
type Report struct {
	// Entity names the table or collection.
	Entity string
	// Rows is how many rows were examined.
	Rows int
	// Reencrypted is how many rows were rewritten.
	Reencrypted int
}
