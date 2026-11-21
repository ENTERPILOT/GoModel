package server

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/usage"
)

// usageCostMember is the member of a usage object that carries the cost. The
// shape follows OpenRouter: usage.cost is a number in USD.
const usageCostMember = "cost"

// usageCostEdit says how a usage object is rewritten: every cost member is
// removed, so whichever duplicate a parser keeps, the only cost left is
// member (`"cost":<amount>`), added at the end, or none when member is empty.
//
// With billed (markups enabled) every other cost the provider reported is
// removed as well, so a reseller's customer learns neither the provider cost
// nor the markup: the members of usage.ProviderCostFields, in the usage object
// and in its raw_usage. Those are the members the usage log reads a provider's
// cost from; a cost a provider reports under another name is not known to be
// one and stays.
type usageCostEdit struct {
	member string
	billed bool
}

// newUsageCostEdit returns the edit that leaves a usage object with this
// gateway's cost, or with no cost when it is unknown.
func newUsageCostEdit(amount float64, known, billed bool) usageCostEdit {
	edit := usageCostEdit{billed: billed}
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

// drops reports whether a member of a usage object named name is removed.
// Names are compared the way a lenient decoder matches them: unescaped (gjson
// does that) and in any case, with ſ folding to s as Go's decoder folds it.
func (e usageCostEdit) drops(name string) bool {
	return strings.EqualFold(name, usageCostMember) ||
		(e.billed && usage.IsProviderCostField(name, false))
}

// dropsRootMember reports whether a member of a response's root named name is
// removed: a provider's cost reported beside the usage object, with billed.
func (e usageCostEdit) dropsRootMember(name string) bool {
	return e.billed && usage.IsProviderCostField(name, true)
}

// cleansRawUsage reports whether the value of a usage member named name is a
// raw_usage object that loses the provider's costs too.
func (e usageCostEdit) cleansRawUsage(name string, value gjson.Result) bool {
	return e.billed && strings.EqualFold(name, usage.RawUsageMember) && value.IsObject()
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
		value      int    // where the member's value starts
		cleaned    string // the value rewritten, when rewritten is set
		rewritten  bool
	}
	var members []member
	changed := false
	object.ForEach(func(key, value gjson.Result) bool {
		m := member{start: key.Index, value: value.Index, end: value.Index + len(value.Raw), drop: e.drops(key.Str)}
		if !m.drop && e.cleansRawUsage(key.Str, value) {
			m.cleaned, m.rewritten = usageCostEdit{billed: true}.apply(value.Raw)
		}
		changed = changed || m.drop || m.rewritten
		members = append(members, m)
		return true
	})
	if !changed && e.member == "" {
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
		if m.rewritten {
			b.WriteString(raw[m.start:m.value])
			b.WriteString(m.cleaned)
		} else {
			b.WriteString(raw[m.start:m.end])
		}
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
