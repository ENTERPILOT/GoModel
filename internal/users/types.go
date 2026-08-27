// Package users manages the gateway's user and group registry. A user binds
// one unique user_path to a display name and group memberships; a group is a
// named, cross-cutting set of users that model access policies can target.
//
// The registry is metadata over the existing user_path mechanism: request
// authorization, budgets, usage, and audit stay keyed by user_path, and a
// request's groups are resolved from its effective user path at check time.
package users

import "time"

// User binds one unique user path to a display name and group memberships.
type User struct {
	ID          string    `json:"id" bson:"_id"`
	UserPath    string    `json:"user_path" bson:"user_path"`
	Name        string    `json:"name,omitempty" bson:"name,omitempty"`
	Description string    `json:"description,omitempty" bson:"description,omitempty"`
	Groups      []string  `json:"groups,omitempty" bson:"groups,omitempty"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

// clone returns a deep copy so snapshot consumers cannot mutate cached slices.
func (u User) clone() User {
	if len(u.Groups) > 0 {
		u.Groups = append([]string(nil), u.Groups...)
	}
	return u
}

// Group is a named, cross-cutting set of users. The name is the identity:
// access policies reference groups by name.
type Group struct {
	Name        string    `json:"name" bson:"_id"`
	Description string    `json:"description,omitempty" bson:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

// UpsertUserInput captures the admin request for creating or updating a user.
// An empty ID creates a new user; a non-empty ID updates the existing one.
type UpsertUserInput struct {
	ID          string
	UserPath    string
	Name        string
	Description string
	Groups      []string
}

// UpsertGroupInput captures the admin request for creating or updating a group.
type UpsertGroupInput struct {
	Name        string
	Description string
}
