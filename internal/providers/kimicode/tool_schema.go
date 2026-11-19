package kimicode

import (
	"maps"
	"math"
	"slices"

	"github.com/goccy/go-json"
)

// Moonshot validates each tool's parameters against Moonshot Flavored JSON
// Schema (MFJS), a subset of JSON Schema, and fails the whole request with a
// 400 "tools.function.parameters is not a valid moonshot flavored json schema"
// when one tool falls outside it. Standard schemas from MCP servers and agent
// frameworks do, so the constructs the validator rejects are rewritten here
// instead (Postel's law).
//
// The rules come from Moonshot's published validator,
// https://github.com/MoonshotAI/walle, at the level the API applies (lite):
//
//   - A boolean schema becomes its object equivalent: true is {}, false is
//     {"not": {}}.
//   - Tuple items (an array of schemas) become one schema accepting any of
//     the members.
//   - An enum gets the type its values imply, because MFJS requires one beside
//     every enum. Values of several types are split into one typed anyOf
//     branch each.
//
// Releases of the validator before v0.1.15 (2026-08) were stricter, and which
// one an endpoint runs is not observable. Their rules are covered where the
// repair is what the current validator's own canonicalization does: enum
// values the declared type rules out are dropped, a type or enum beside anyOf
// moves into the branches, and required names missing from properties are
// pruned. A type or enum beside $ref is dropped, since the definition cannot
// take it without changing for every other reference.
//
// Keywords MFJS does not support (oneOf, allOf, const, format and so on) are
// accepted and ignored upstream, so they pass through untouched. A schema
// that cannot be repaired, such as one with an unresolvable $ref, is forwarded
// as it is and the upstream reports it.

// sanitizeTools adapts every function tool's parameters to MFJS and reports
// whether anything changed. The argument is never mutated; it is returned as
// it came when no schema needs a repair. Both tool shapes are covered: Chat
// Completions nests the schema under "function", the Responses API carries it
// on the tool itself.
func sanitizeTools(tools []map[string]any) ([]map[string]any, bool) {
	var out []map[string]any
	for i, tool := range tools {
		repaired, changed := sanitizeTool(tool)
		if !changed {
			continue
		}
		if out == nil {
			out = slices.Clone(tools)
		}
		out[i] = repaired
	}
	if out == nil {
		return tools, false
	}
	return out, true
}

func sanitizeTool(tool map[string]any) (map[string]any, bool) {
	function, nested := tool["function"].(map[string]any)
	if !nested {
		return sanitizeParameters(tool)
	}
	repaired, changed := sanitizeParameters(function)
	if !changed {
		return tool, false
	}
	out := maps.Clone(tool)
	out["function"] = repaired
	return out, true
}

// sanitizeParameters repairs the "parameters" schema of a function
// definition, returning a copy of the definition when the schema changed.
func sanitizeParameters(function map[string]any) (map[string]any, bool) {
	repaired, changed := sanitizeSubschema(function["parameters"])
	if !changed {
		return function, false
	}
	out := maps.Clone(function)
	out["parameters"] = repaired
	return out, true
}

// schemaEdit is a copy-on-write view of one schema object: the map is cloned
// on the first change, so a schema that needs no repair is returned as it came.
type schemaEdit struct {
	node    map[string]any
	changed bool
}

func (e *schemaEdit) own() {
	if !e.changed {
		e.node = maps.Clone(e.node)
		e.changed = true
	}
}

func (e *schemaEdit) set(key string, value any) {
	e.own()
	e.node[key] = value
}

func (e *schemaEdit) drop(key string) {
	if _, ok := e.node[key]; ok {
		e.own()
		delete(e.node, key)
	}
}

// sanitizeSchema repairs one schema object and everything nested in the
// keywords the validator descends into. The walk is keyword-aware, so a
// property that happens to be named "enum" or "required" stays a property.
func sanitizeSchema(in map[string]any) (map[string]any, bool) {
	e := &schemaEdit{node: in}

	// These run before the walk, so a branch that was handed a type or an enum
	// is repaired with it.
	repairAnyOfSiblings(e)
	repairRefSiblings(e)
	repairEnum(e)

	// "definitions" is where draft-07 keeps what later drafts call "$defs";
	// the validator resolves references into both.
	for _, key := range []string{"properties", "$defs", "definitions"} {
		if members, ok := e.node[key].(map[string]any); ok {
			if repaired, changed := sanitizeSchemaMap(members); changed {
				e.set(key, repaired)
			}
		}
	}
	if branches, ok := e.node["anyOf"].([]any); ok {
		if repaired, changed := sanitizeSchemaList(branches); changed {
			e.set("anyOf", repaired)
		}
	}
	if additional, ok := e.node["additionalProperties"].(map[string]any); ok {
		if repaired, changed := sanitizeSchema(additional); changed {
			e.set("additionalProperties", repaired)
		}
	}
	repairItems(e)
	repairRequired(e)
	return e.node, e.changed
}

// sanitizeSubschema repairs a value in a schema position. Anything that is
// neither an object nor a boolean is not a schema and is left for the upstream
// to report.
func sanitizeSubschema(value any) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return sanitizeSchema(v)
	case bool:
		if v {
			return map[string]any{}, true
		}
		return map[string]any{"not": map[string]any{}}, true
	}
	return value, false
}

func sanitizeSchemaMap(members map[string]any) (map[string]any, bool) {
	var out map[string]any
	for name, member := range members {
		repaired, changed := sanitizeSubschema(member)
		if !changed {
			continue
		}
		if out == nil {
			out = maps.Clone(members)
		}
		out[name] = repaired
	}
	if out == nil {
		return members, false
	}
	return out, true
}

func sanitizeSchemaList(members []any) ([]any, bool) {
	var out []any
	for i, member := range members {
		repaired, changed := sanitizeSubschema(member)
		if !changed {
			continue
		}
		if out == nil {
			out = slices.Clone(members)
		}
		out[i] = repaired
	}
	if out == nil {
		return members, false
	}
	return out, true
}

// repairItems turns tuple items into the single schema MFJS expects. The
// positions are lost: every element may match any member.
func repairItems(e *schemaEdit) {
	tuple, ok := e.node["items"].([]any)
	if !ok {
		if repaired, changed := sanitizeSubschema(e.node["items"]); changed {
			e.set("items", repaired)
		}
		return
	}
	members, _ := sanitizeSchemaList(tuple)
	switch len(members) {
	case 0:
		e.drop("items")
	case 1:
		e.set("items", members[0])
	default:
		e.set("items", map[string]any{"anyOf": members})
	}
}

// enumTypes orders the types an enum is split into.
var enumTypes = []string{"string", "integer", "number", "boolean", "object", "array"}

// repairEnum makes an enum satisfy MFJS: the schema declares a type and the
// values share it. Only a scalar type may be paired with null.
func repairEnum(e *schemaEdit) {
	values, ok := e.node["enum"].([]any)
	if !ok {
		return
	}
	declared := typeNames(e.node["type"])
	ruledOut := func(value any) bool {
		kind := enumType(value)
		return len(declared) != 0 && !slices.Contains(declared, kind) &&
			(kind != "integer" || !slices.Contains(declared, "number"))
	}
	if len(values) > 0 && enumTypeDeclared(declared) && !slices.ContainsFunc(values, ruledOut) {
		return // the common case: a typed enum needs nothing
	}

	// Integers and other numbers form one numeric type, as [2, 3.33] does.
	byType := map[string][]any{}
	kept, fractional := 0, false
	for _, value := range values {
		kind := enumType(value)
		if kind == "" {
			return // not a value JSON decoding produces; leave the enum alone
		}
		if ruledOut(value) {
			continue
		}
		if kind == "integer" || kind == "number" {
			fractional = fractional || kind == "number"
			kind = "number"
		}
		byType[kind] = append(byType[kind], value)
		kept++
	}
	if kept == 0 {
		e.drop("enum")
		return
	}
	if numbers, ok := byType["number"]; ok && !fractional {
		byType["integer"] = numbers
		delete(byType, "number")
	}
	var types []string
	for _, name := range enumTypes {
		if byType[name] != nil {
			types = append(types, name)
		}
	}
	nullable := byType["null"] != nil
	composite := slices.Contains(types, "object") || slices.Contains(types, "array")

	if len(types) > 1 || (nullable && composite) {
		branches := make([]any, 0, len(types)+1)
		for _, name := range types {
			branches = append(branches, map[string]any{"type": name, "enum": byType[name]})
		}
		if nullable {
			branches = append(branches, map[string]any{"type": "null"})
		}
		e.set("anyOf", branches)
		e.drop("enum")
		e.drop("type")
		return
	}

	if kept != len(values) {
		e.set("enum", slices.DeleteFunc(slices.Clone(values), ruledOut))
	}
	if enumTypeDeclared(declared) {
		return
	}
	switch {
	case len(types) == 0:
		e.set("type", "null")
	case !nullable:
		e.set("type", types[0])
	default:
		// null goes first: validators before v0.1.15 fail a null literal
		// against a numeric type listed ahead of it.
		e.set("type", []any{"null", types[0]})
	}
}

// enumType names the JSON Schema type of an enum literal, or "" for a value
// JSON decoding does not produce.
func enumType(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return ""
}

// typeNames reads a "type" keyword: a name or a list of names.
func typeNames(declared any) []string {
	switch t := declared.(type) {
	case string:
		return []string{t}
	case []any:
		names := make([]string, 0, len(t))
		for _, item := range t {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

// enumTypeDeclared reports whether the declared type is one MFJS accepts
// beside an enum: a single type, or a scalar paired with null.
func enumTypeDeclared(declared []string) bool {
	switch len(declared) {
	case 1:
		return true
	case 2:
		return declared[0] != declared[1] && slices.Contains(declared, "null") &&
			!slices.Contains(declared, "object") && !slices.Contains(declared, "array")
	}
	return false
}

// repairAnyOfSiblings moves a type and an enum from beside anyOf into the
// branches: older validators reject a type there, and an enum needs one.
// JSON Schema applies both together with whichever branch matches, so every
// branch is limited by them. A branch whose own type shares nothing with the
// outer one can never match and is dropped. A branch that is a reference is
// left to its definition, and a branch keeps an enum of its own.
func repairAnyOfSiblings(e *schemaEdit) {
	branches, ok := e.node["anyOf"].([]any)
	if !ok {
		return
	}
	outerType, typed := e.node["type"]
	values, enumerated := e.node["enum"].([]any)
	if !typed && !enumerated {
		return
	}

	limited := make([]any, 0, len(branches))
	for _, member := range branches {
		branch, ok := member.(map[string]any)
		if member == true {
			branch, ok = map[string]any{}, true
		}
		if !ok || branch["$ref"] != nil {
			limited = append(limited, member)
			continue
		}
		branch = maps.Clone(branch)
		if typed {
			narrowed, overlaps := narrowType(outerType, branch["type"])
			if !overlaps {
				continue
			}
			branch["type"] = narrowed
		}
		if enumerated && branch["enum"] == nil {
			branch["enum"] = values
		}
		limited = append(limited, branch)
	}
	if len(limited) == 0 {
		// No branch can match; the type and enum are what is left to say.
		e.drop("anyOf")
		return
	}
	e.set("anyOf", limited)
	e.drop("type")
	e.drop("enum")
}

// narrowType limits a branch's own type to what the type beside its anyOf
// admits, treating integer as part of number. It reports false when the two
// share nothing. A branch without a type takes the outer one.
func narrowType(outer, own any) (any, bool) {
	if own == nil {
		return outer, true
	}
	outerNames, ownNames := typeNames(outer), typeNames(own)
	if len(outerNames) == 0 || len(ownNames) == 0 {
		return own, true // not a type this code can read; leave it as written
	}
	shared := make([]any, 0, len(ownNames))
	narrowed := false
	for _, name := range ownNames {
		switch {
		case slices.Contains(outerNames, name), name == "integer" && slices.Contains(outerNames, "number"):
			shared = append(shared, name)
		case name == "number" && slices.Contains(outerNames, "integer"):
			shared = append(shared, "integer")
			narrowed = true
		default:
			narrowed = true
		}
	}
	switch {
	case len(shared) == 0:
		return nil, false
	case !narrowed:
		return own, true
	case len(shared) == 1:
		return shared[0], true
	}
	return shared, true
}

// repairRefSiblings drops a type and an enum from beside $ref. Older
// validators reject the type, and an enum cannot stay without one. The
// referenced definition applies on its own, which may accept more.
func repairRefSiblings(e *schemaEdit) {
	if e.node["$ref"] != nil {
		e.drop("type")
		e.drop("enum")
	}
}

// repairRequired prunes required names the schema does not declare under
// properties, which older validators reject.
func repairRequired(e *schemaEdit) {
	required, ok := e.node["required"].([]any)
	if !ok {
		return
	}
	properties, _ := e.node["properties"].(map[string]any)
	undeclared := func(name any) bool {
		key, ok := name.(string)
		if !ok {
			return true
		}
		_, declared := properties[key]
		return !declared
	}
	if !slices.ContainsFunc(required, undeclared) {
		return
	}
	kept := slices.DeleteFunc(slices.Clone(required), undeclared)
	if len(kept) == 0 {
		e.drop("required")
		return
	}
	e.set("required", kept)
}
