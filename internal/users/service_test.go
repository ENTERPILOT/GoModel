package users

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func newTestService(t *testing.T, db sqlx.DB) *Service {
	t.Helper()
	store, err := NewSQLStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	svc, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return svc
}

func runServiceTest(t *testing.T, body func(t *testing.T, svc *Service)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		body(t, newTestService(t, db))
	})
}

// mustGroup creates a group or fails the test.
func mustGroup(t *testing.T, svc *Service, name, parent string) Group {
	t.Helper()
	group, err := svc.UpsertGroup(context.Background(), UpsertGroupInput{Name: name, Parent: parent})
	if err != nil {
		t.Fatalf("UpsertGroup(%s) error = %v", name, err)
	}
	return group
}

// mustUser creates a user or fails the test.
func mustUser(t *testing.T, svc *Service, name, group string) User {
	t.Helper()
	user, err := svc.UpsertUser(context.Background(), UpsertUserInput{Name: name, Group: group})
	if err != nil {
		t.Fatalf("UpsertUser(%s) error = %v", name, err)
	}
	return user
}

func TestServiceUpsertUserDerivesPath(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()

		engineering := mustGroup(t, svc, "engineering", "")
		if engineering.Path != "/engineering" {
			t.Fatalf("group path = %q, want /engineering", engineering.Path)
		}
		platform := mustGroup(t, svc, "platform", "engineering")
		if platform.Path != "/engineering/platform" {
			t.Fatalf("nested group path = %q, want /engineering/platform", platform.Path)
		}

		anna := mustUser(t, svc, " anna ", "platform")
		if anna.UserPath != "/engineering/platform/anna" || anna.Name != "anna" {
			t.Fatalf("user = %+v, want derived /engineering/platform/anna", anna)
		}
		root := mustUser(t, svc, "admin", "")
		if root.UserPath != "/admin" {
			t.Fatalf("root user path = %q, want /admin", root.UserPath)
		}

		// Validation failures.
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{Name: ""}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(empty name) error = %v, want validation error", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{Name: "a/b"}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(name with slash) error = %v, want validation error", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{Name: "x", Group: "nope"}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(unknown group) error = %v, want validation error", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{Name: "anna", Group: "platform"}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(duplicate path) error = %v, want validation error", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{Name: "platform", Group: "engineering"}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(collides with group path) error = %v, want validation error", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{ID: "missing", Name: "x"}); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("UpsertUser(unknown id) error = %v, want ErrUserNotFound", err)
		}

		// Moving a user to another group re-derives the path and keeps CreatedAt.
		moved, err := svc.UpsertUser(ctx, UpsertUserInput{ID: anna.ID, Name: "anna", Group: "engineering"})
		if err != nil {
			t.Fatalf("UpsertUser(move) error = %v", err)
		}
		if moved.UserPath != "/engineering/anna" || !moved.CreatedAt.Equal(anna.CreatedAt) {
			t.Fatalf("UpsertUser(move) = %+v", moved)
		}

		if err := svc.DeleteUser(ctx, anna.ID); err != nil {
			t.Fatalf("DeleteUser() error = %v", err)
		}
		if got := len(svc.ListUsers()); got != 1 {
			t.Fatalf("ListUsers() after delete len = %d, want 1", got)
		}
	})
}

func TestServiceGroupTreeValidation(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()

		mustGroup(t, svc, "a", "")
		mustGroup(t, svc, "b", "a")
		mustGroup(t, svc, "c", "b")

		if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: "x", Parent: "nope"}); !IsValidationError(err) {
			t.Fatalf("UpsertGroup(unknown parent) error = %v, want validation error", err)
		}
		if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: "a", Parent: "a"}); !IsValidationError(err) {
			t.Fatalf("UpsertGroup(self parent) error = %v, want validation error", err)
		}
		// Moving "a" under its own descendant "c" would create a cycle.
		if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: "a", Parent: "c"}); !IsValidationError(err) {
			t.Fatalf("UpsertGroup(cycle) error = %v, want validation error", err)
		}
	})
}

func TestServiceGroupMoveRewritesUserPaths(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()

		mustGroup(t, svc, "engineering", "")
		mustGroup(t, svc, "platform", "engineering")
		anna := mustUser(t, svc, "anna", "platform")
		bob := mustUser(t, svc, "bob", "engineering")

		// Move platform to the root: anna follows, bob stays.
		moved := mustGroup(t, svc, "platform", "")
		if moved.Path != "/platform" {
			t.Fatalf("moved group path = %q, want /platform", moved.Path)
		}
		annaAfter, ok := svc.UserByID(anna.ID)
		if !ok || annaAfter.UserPath != "/platform/anna" {
			t.Fatalf("member path after move = %+v, want /platform/anna", annaAfter)
		}
		bobAfter, ok := svc.UserByID(bob.ID)
		if !ok || bobAfter.UserPath != "/engineering/bob" {
			t.Fatalf("non-member path after move = %+v, want unchanged", bobAfter)
		}

		// A move whose new group path collides with an existing user path is
		// rejected up front: user /engineering/tools blocks moving the root
		// group "tools" under "engineering".
		mustGroup(t, svc, "tools", "")
		tools := mustUser(t, svc, "tools", "engineering")
		if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: "tools", Parent: "engineering"}); !IsValidationError(err) {
			t.Fatalf("UpsertGroup(colliding move) error = %v, want validation error", err)
		}
		// Nothing moved: paths are unchanged.
		if after, _ := svc.UserByID(tools.ID); after.UserPath != "/engineering/tools" {
			t.Fatalf("path after rejected move = %q, want /engineering/tools", after.UserPath)
		}
	})
}

func TestServiceGroupsForPathMatchesGroupChain(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		mustGroup(t, svc, "engineering", "")
		mustGroup(t, svc, "platform", "engineering")
		mustUser(t, svc, "anna", "platform")

		if got := svc.GroupsForPath("/engineering/platform/anna/service"); !reflect.DeepEqual(got, []string{"engineering", "platform"}) {
			t.Fatalf("GroupsForPath(descendant) = %v, want [engineering platform]", got)
		}
		if got := svc.GroupsForPath("/engineering"); !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Fatalf("GroupsForPath(group path) = %v, want [engineering]", got)
		}
		if got := svc.GroupsForPath("/sales/anyone"); got != nil {
			t.Fatalf("GroupsForPath(unknown) = %v, want nil", got)
		}
		if got := svc.GroupsForPath(""); got != nil {
			t.Fatalf("GroupsForPath(empty) = %v, want nil", got)
		}
	})
}

func TestServiceDeleteGroupRefusesNonEmpty(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()

		mustGroup(t, svc, "engineering", "")
		mustGroup(t, svc, "platform", "engineering")
		anna := mustUser(t, svc, "anna", "platform")

		if err := svc.DeleteGroup(ctx, "engineering"); !IsValidationError(err) {
			t.Fatalf("DeleteGroup(with subgroup) error = %v, want validation error", err)
		}
		if err := svc.DeleteGroup(ctx, "platform"); !IsValidationError(err) {
			t.Fatalf("DeleteGroup(with member) error = %v, want validation error", err)
		}
		if err := svc.DeleteGroup(ctx, "missing"); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("DeleteGroup(missing) error = %v, want ErrGroupNotFound", err)
		}

		if err := svc.DeleteUser(ctx, anna.ID); err != nil {
			t.Fatalf("DeleteUser() error = %v", err)
		}
		if err := svc.DeleteGroup(ctx, "platform"); err != nil {
			t.Fatalf("DeleteGroup(emptied) error = %v", err)
		}
		if err := svc.DeleteGroup(ctx, "engineering"); err != nil {
			t.Fatalf("DeleteGroup(root) error = %v", err)
		}
	})
}

func TestNormalizeGroupNames(t *testing.T) {
	t.Parallel()
	got, err := NormalizeGroupNames([]string{" beta ", "alpha", "beta", "", " "})
	if err != nil {
		t.Fatalf("NormalizeGroupNames() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("NormalizeGroupNames() = %v, want [alpha beta]", got)
	}
	if _, err := NormalizeGroupNames([]string{"a/b"}); !IsValidationError(err) {
		t.Fatalf("NormalizeGroupNames(slash) error = %v, want validation error", err)
	}
}

func TestServiceUserByIDAndUserIDForPath(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		mustGroup(t, svc, "team", "")
		alpha := mustUser(t, svc, "alpha", "team")

		got, ok := svc.UserByID(alpha.ID)
		if !ok || got.UserPath != "/team/alpha" {
			t.Fatalf("UserByID(%q) = %+v, %v", alpha.ID, got, ok)
		}
		if _, ok := svc.UserByID("missing"); ok {
			t.Fatalf("UserByID(missing) = _, true, want false")
		}

		// Exact and descendant paths attribute to the registered user.
		if id := svc.UserIDForPath("/team/alpha"); id != alpha.ID {
			t.Fatalf("UserIDForPath(exact) = %q, want %q", id, alpha.ID)
		}
		if id := svc.UserIDForPath("/team/alpha/service"); id != alpha.ID {
			t.Fatalf("UserIDForPath(descendant) = %q, want %q", id, alpha.ID)
		}
		if id := svc.UserIDForPath("/team/other"); id != "" {
			t.Fatalf("UserIDForPath(sibling) = %q, want empty", id)
		}
		if id := svc.UserIDForPath(""); id != "" {
			t.Fatalf("UserIDForPath(empty) = %q, want empty", id)
		}
	})
}
