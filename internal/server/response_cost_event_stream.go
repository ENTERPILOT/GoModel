package server

import (
	"bytes"
	"io"

	"github.com/enterpilot/gomodel/internal/usage"
)

// eventCostStream forgets the reported cost when a paced stream hands out a
// usage object, before the usage observer reads it. The cost is known again
// once the observer priced that usage, so an event whose usage it does not
// read (one it skips, or cannot parse) carries no cost, never the cost of an
// event before it. Events without usage leave the cost as it is: a stream
// converted for the client carries its usage in an event written after them.
type eventCostStream struct {
	io.ReadCloser
	report *usage.CostReport
	usage  usageObjectFinder
}

func (s *eventCostStream) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if s.usage.find(p[:n]) {
		s.report.Forget()
	}
	return n, err
}

// usageObjectFinder is usage.HasUsageObject for a stream read in pieces: it
// finds a "usage" member whose value is an object wherever the reads split
// it, in the name, around the colon or in front of the brace.
type usageObjectFinder struct {
	named int  // how much of the name the bytes read so far end with; all of it while its value is due
	colon bool // the colon after the name was read
}

const usageMemberName = `"usage"`

// find reports whether data, which continues the data of the calls before,
// holds or completes such a member. A line that ends after the name or the
// colon counts as one: the value then starts on the next data line of the
// event, where the usage observer reads it joined to this one.
func (f *usageObjectFinder) find(data []byte) bool {
	found := false
	for i := 0; i < len(data); i++ {
		if f.named == 0 {
			// Nothing but a quote starts the name: a long string is not
			// read bytewise.
			skip := bytes.IndexByte(data[i:], '"')
			if skip == -1 {
				break
			}
			i += skip
		}
		c := data[i]
		switch {
		case f.named < len(usageMemberName) && c == usageMemberName[f.named]:
			f.named++
			continue
		case f.named < len(usageMemberName):
		case c == '\n', c == '\r', c == '{' && f.colon:
			found = true
		case isJSONSpace(c):
			continue
		case c == ':' && !f.colon:
			f.colon = true
			continue
		}
		f.named, f.colon = 0, false
		if c == '"' {
			f.named = 1
		}
	}
	return found
}
