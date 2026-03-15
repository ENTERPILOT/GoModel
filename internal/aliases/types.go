package aliases

import (
	"time"

	"gomodel/internal/core"
)

// Alias maps a gateway-visible alias name to a concrete model selector.
type Alias struct {
	Name           string    `json:"name" bson:"name"`
	TargetModel    string    `json:"target_model" bson:"target_model"`
	TargetProvider string    `json:"target_provider,omitempty" bson:"target_provider,omitempty"`
	Description    string    `json:"description,omitempty" bson:"description,omitempty"`
	Enabled        bool      `json:"enabled" bson:"enabled"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
}

// TargetSelector returns the concrete selector this alias points to.
func (a Alias) TargetSelector() (core.ModelSelector, error) {
	return core.ParseModelSelector(a.TargetModel, a.TargetProvider)
}

// Resolution captures the requested selector and the concrete selector chosen after alias resolution.
type Resolution struct {
	Requested core.ModelSelector `json:"requested"`
	Resolved  core.ModelSelector `json:"resolved"`
	Alias     *Alias             `json:"alias,omitempty"`
}

// View is the admin-facing representation of an alias with current validity status.
type View struct {
	Alias
	ResolvedModel string `json:"resolved_model"`
	ProviderType  string `json:"provider_type,omitempty"`
	Valid         bool   `json:"valid"`
}
