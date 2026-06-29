// Package failover stores operator-managed manual failover rules.
package failover

import (
	"strings"
	"time"
)

const (
	ManagedSourceDashboard = "dashboard"
	ManagedSourceConfig    = "config"
)

// Rule is one manual failover rule for a source model selector.
type Rule struct {
	Source        string    `json:"source" bson:"_id"`
	Targets       []string  `json:"targets" bson:"targets"`
	Description   string    `json:"description,omitempty" bson:"description,omitempty"`
	Enabled       bool      `json:"enabled" bson:"enabled"`
	ManagedSource string    `json:"managed_source" bson:"managed_source"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

// View is the admin-facing representation of one failover rule.
type View struct {
	Source        string    `json:"source"`
	Targets       []string  `json:"targets"`
	Description   string    `json:"description,omitempty"`
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
		Description:   r.Description,
		Enabled:       r.Enabled,
		Managed:       strings.TrimSpace(r.ManagedSource) != "" && r.ManagedSource != ManagedSourceDashboard,
		ManagedSource: r.ManagedSource,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}
