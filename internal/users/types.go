// Package users manages the gateway's user and group registry. Groups form a
// tree (each group has at most one parent); a user belongs to at most one
// group. A user's user_path is derived from its group chain plus its name, so
// the path hierarchy always mirrors the group tree.
//
// The registry is metadata over the existing user_path mechanism: request
// authorization, budgets, usage, and audit stay keyed by user_path, and a
// request's groups are resolved from its effective user path at check time.
// Identity is the stable user id; the derived path is placement, recomputed
// when the tree changes.
package users

import "time"

// User is one registered caller. UserPath is derived — the owning group's
// path plus the user's name — and is rewritten automatically when the group
// moves in the tree.
type User struct {
	ID          string `json:"id" bson:"_id"`
	UserPath    string `json:"user_path" bson:"user_path"`
	Name        string `json:"name" bson:"name"`
	Description string `json:"description,omitempty" bson:"description,omitempty"`
	// Group names the owning group; empty means the user sits at the root.
	Group     string    `json:"group,omitempty" bson:"group,omitempty"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// Group is one node of the group tree. The name is the identity: access
// policies reference groups by name, and names are globally unique.
type Group struct {
	Name        string `json:"name" bson:"_id"`
	Description string `json:"description,omitempty" bson:"description,omitempty"`
	// Parent names the parent group; empty means a root group.
	Parent string `json:"parent,omitempty" bson:"parent,omitempty"`
	// Path is the derived hierarchy path (/parent/.../name). Computed from
	// the tree on load, never stored.
	Path      string    `json:"path,omitempty" bson:"-"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// UpsertUserInput captures the admin request for creating or updating a user.
// An empty ID creates a new user; a non-empty ID updates the existing one.
type UpsertUserInput struct {
	ID          string
	Name        string
	Description string
	Group       string
}

// UpsertGroupInput captures the admin request for creating or updating a group.
type UpsertGroupInput struct {
	Name        string
	Description string
	Parent      string
}
