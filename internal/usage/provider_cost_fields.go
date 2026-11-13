package usage

import "strings"

// Names of the members a provider reports the cost of a request in.
const (
	providerCostMember        = "cost"              // OpenRouter credits; Eden AI, at the response root
	providerCostTicksMember   = "cost_in_usd_ticks" // xAI
	providerCostDetailsMember = "cost_details"      // OpenRouter's breakdown of its cost
)

// RawUsageMember is the member of a usage object that holds the provider's
// usage members without a typed field of their own (core's RawUsage), where a
// typed response or stream carries them nested instead of beside the token
// counts. The members of ProviderCostFields that belong to a usage object are
// found in it too.
const RawUsageMember = "raw_usage"

// ProviderCostField is one member in which a provider reports what a request
// cost.
type ProviderCostField struct {
	Name string
	// Root places the member at the root of a response or stream event; every
	// other field is a member of its usage object.
	Root bool
	// Object marks a breakdown object; every other field is a number.
	Object bool
}

// providerCostFields is every provider-reported cost the gateway knows. The
// usage readers take a provider's cost from these members and from no other
// (see copyProviderCosts and CalculateUsageCost), and a response that must not
// reveal the provider cost loses exactly these (internal/server), so reading
// a new one here is what removes it there.
var providerCostFields = []ProviderCostField{
	{Name: providerCostMember},
	{Name: providerCostTicksMember},
	{Name: providerCostDetailsMember, Object: true},
	{Name: providerCostMember, Root: true},
}

// ProviderCostFields returns every member a provider reports a cost in.
func ProviderCostFields() []ProviderCostField {
	return append([]ProviderCostField(nil), providerCostFields...)
}

// IsProviderCostField reports whether a member named name, of a usage object
// or with root of a response's root, is a provider-reported cost. Names are
// compared the way a lenient decoder matches them: in any letter case.
func IsProviderCostField(name string, root bool) bool {
	for _, field := range providerCostFields {
		if field.Root == root && strings.EqualFold(name, field.Name) {
			return true
		}
	}
	return false
}

// copyProviderCosts copies the provider-reported costs among the members of
// object, a usage object or with root an event's root, into rawData. A cost
// already there stays: the usage object is read first and is the more specific
// word. A negative or non-numeric cost (a NaN included) is dropped.
func copyProviderCosts(rawData, object map[string]any, root bool) {
	for _, field := range providerCostFields {
		value, reported := object[field.Name]
		if field.Root != root || !reported {
			continue
		}
		if _, exists := rawData[field.Name]; exists {
			continue
		}
		if field.Object {
			if details, ok := nestedUsageMap(value); ok {
				rawData[field.Name] = details
			}
			continue
		}
		if number, ok := numericFloat(value); ok && number >= 0 {
			rawData[field.Name] = number
		}
	}
}

// isProviderCostMember reports whether a usage member is one copyProviderCosts
// reads, by its exact name.
func isProviderCostMember(name string) bool {
	for _, field := range providerCostFields {
		if !field.Root && field.Name == name {
			return true
		}
	}
	return false
}
