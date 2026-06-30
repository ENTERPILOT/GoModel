// Package failover stores operator-managed manual failover mappings.
package failover

import (
	"strings"
	"time"
)

const (
	ManagedSourceDashboard = "dashboard"
	ManagedSourceConfig    = "config"
)

// Rule is one manual failover mapping for a primary model selector.
type Rule struct {
	Source        string    `json:"primary_model" bson:"_id"`
	Targets       []string  `json:"fallback_models" bson:"fallback_models"`
	Enabled       bool      `json:"enabled" bson:"enabled"`
	ManagedSource string    `json:"managed_source" bson:"managed_source"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

// View is the admin-facing representation of one failover mapping.
type View struct {
	Source        string    `json:"primary_model"`
	Targets       []string  `json:"fallback_models"`
	Enabled       bool      `json:"enabled"`
	Managed       bool      `json:"managed"`
	ManagedSource string    `json:"managed_source"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (r Rule) clone() Rule {
	if len(r.Targets) > 0 {
		r.Targets = append([]string(nil), r.Targets...)
	}
	return r
}

func (r Rule) view() View {
	return View{
		Source:        r.Source,
		Targets:       append([]string(nil), r.Targets...),
		Enabled:       r.Enabled,
		Managed:       strings.TrimSpace(r.ManagedSource) != "" && r.ManagedSource != ManagedSourceDashboard,
		ManagedSource: r.ManagedSource,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}
