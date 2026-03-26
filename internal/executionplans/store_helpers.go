package executionplans

import "fmt"

type versionRowScanner interface {
	Scan(dest ...any) error
}

type versionRowIterator interface {
	versionRowScanner
	Next() bool
	Err() error
}

func collectVersions(rows versionRowIterator, scan func(versionRowScanner) (Version, error)) ([]Version, error) {
	versions := make([]Version, 0)
	for rows.Next() {
		version, err := scan(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate execution plans: %w", err)
	}
	return versions, nil
}
