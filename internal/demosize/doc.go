// Package demosize is a dummy package used only to demo the PR size
// label workflow. It carries no production behavior.
package demosize

// Line is a trivial demo value object.
type Line struct {
	Index int
	Text  string
}

// NewLine returns a demo Line.
func NewLine(index int, text string) Line {
	return Line{Index: index, Text: text}
}

// Count returns how many demo lines exist.
func Count(lines []Line) int {
	return len(lines)
}
// end
