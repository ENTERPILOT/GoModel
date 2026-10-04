package encryption

import "fmt"

// Field points at one secret value of an entity. Name is the field name bound
// into the value's additional authenticated data.
type Field struct {
	Name  string
	Value *string
}

// SealFields seals every field of one entity in place, all with the same
// data key: the active one is looked up once per entity, not per field.
func (b *Box) SealFields(kind, id string, fields ...Field) error {
	if !b.Enabled() || !hasValue(fields) {
		return nil
	}
	active, aead, err := b.currentSealingKey()
	if err != nil {
		return fmt.Errorf("encrypt %s %q: %w", kind, id, err)
	}
	for _, field := range fields {
		if *field.Value == "" {
			continue
		}
		sealed, err := sealWith(active, aead, AAD(kind, id, field.Name), *field.Value)
		if err != nil {
			return fmt.Errorf("encrypt %s %q field %s: %w", kind, id, field.Name, err)
		}
		*field.Value = sealed
	}
	return nil
}

func hasValue(fields []Field) bool {
	for _, field := range fields {
		if *field.Value != "" {
			return true
		}
	}
	return false
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
	// Skipped is how many rows kept changing under the pass and were left
	// for the next run.
	Skipped int
}

// maxRowAttempts bounds how often a re-encryption pass re-reads a row whose
// secrets changed between its read and its conditional write.
const maxRowAttempts = 3

// RowOutcome is what re-encrypting one row did.
type RowOutcome int

const (
	// RowUnchanged: the row needed no rewrite, or no longer exists.
	RowUnchanged RowOutcome = iota
	// RowRewritten: the row's secrets were re-sealed.
	RowRewritten
	// RowConflict: the row's secrets changed between the read and the
	// conditional write, so nothing was written.
	RowConflict
)

// ReencryptRows runs rewrite for each row and tallies the outcomes. A row in
// conflict is re-read and retried a few times, then counted as skipped.
func ReencryptRows(entity string, names []string, rewrite func(name string) (RowOutcome, error)) (Report, error) {
	report := Report{Entity: entity, Rows: len(names)}
	for _, name := range names {
		outcome := RowConflict
		for attempt := 0; attempt < maxRowAttempts && outcome == RowConflict; attempt++ {
			var err error
			if outcome, err = rewrite(name); err != nil {
				return report, err
			}
		}
		switch outcome {
		case RowRewritten:
			report.Reencrypted++
		case RowConflict:
			report.Skipped++
		}
	}
	return report, nil
}
