package server

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Members of a usage object that carry a cost. The shape follows OpenRouter:
// usage.cost is a number in USD and usage.cost_details breaks it down.
const (
	usageCostMember        = "cost"
	usageCostDetailsMember = "cost_details"
)

// usageCostEdit says how a usage object is rewritten: every cost member is
// removed, so whichever duplicate a parser keeps, the only cost left is
// member (`"cost":<amount>`), added at the end, or none when member is empty.
// With stripDetails (markups enabled) every cost_details member is removed
// too, so a provider cost never reaches a reseller's customer.
type usageCostEdit struct {
	member       string
	stripDetails bool
}

// newUsageCostEdit returns the edit that leaves a usage object with this
// gateway's cost, or with no cost when it is unknown.
func newUsageCostEdit(amount float64, known, stripDetails bool) usageCostEdit {
	edit := usageCostEdit{stripDetails: stripDetails}
	if known {
		edit.member = `"` + usageCostMember + `":` + formatResponseCost(amount)
	}
	return edit
}

// carrying returns the edit for a usage object that carries the cost when
// carries is set, and otherwise the one that only removes members.
func (e usageCostEdit) carrying(carries bool) usageCostEdit {
	if !carries {
		e.member = ""
	}
	return e
}

// drops reports whether a member named name is removed. Names are compared
// the way a lenient decoder matches them: unescaped (gjson does that) and in
// any case, with ſ folding to s as Go's decoder folds it.
func (e usageCostEdit) drops(name string) bool {
	return strings.EqualFold(name, usageCostMember) ||
		(e.stripDetails && strings.EqualFold(name, usageCostDetailsMember))
}

// apply returns the usage object raw rewritten by the edit, or false when it
// stays as it is. raw must be a JSON object. Every member that is kept, and
// the separator in front of it, is copied byte for byte, so nothing else in
// the object is reordered, re-escaped or re-formatted, and no number loses
// precision.
func (e usageCostEdit) apply(raw string) (string, bool) {
	if !gjson.Valid(raw) {
		return "", false
	}
	object := gjson.Parse(raw)
	if !object.IsObject() || len(object.Raw) != len(raw) {
		return "", false
	}
	type member struct {
		start, end int
		drop       bool
	}
	var members []member
	dropped := false
	object.ForEach(func(key, value gjson.Result) bool {
		m := member{start: key.Index, end: value.Index + len(value.Raw), drop: e.drops(key.Str)}
		dropped = dropped || m.drop
		members = append(members, m)
		return true
	})
	if !dropped && e.member == "" {
		return "", false
	}
	if len(members) == 0 {
		return raw[:len(raw)-1] + e.member + "}", true
	}
	var b strings.Builder
	b.Grow(len(raw) + len(e.member) + 1)
	b.WriteString(raw[:members[0].start])
	kept := 0
	for i, m := range members {
		if m.drop {
			continue
		}
		if kept > 0 {
			b.WriteString(raw[members[i-1].end:m.start])
		}
		b.WriteString(raw[m.start:m.end])
		kept++
	}
	if e.member != "" {
		if kept > 0 {
			b.WriteByte(',')
		}
		b.WriteString(e.member)
	}
	b.WriteString(raw[members[len(members)-1].end:])
	return b.String(), true
}
