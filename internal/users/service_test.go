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

func TestServiceUpsertUserLifecycle(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()

		if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: " beta-testers ", Description: "early"}); err != nil {
			t.Fatalf("UpsertGroup() error = %v", err)
		}

		created, err := svc.UpsertUser(ctx, UpsertUserInput{
			UserPath: "team/alpha",
			Name:     "  Alpha  ",
			Groups:   []string{"beta-testers", "beta-testers", " "},
		})
		if err != nil {
			t.Fatalf("UpsertUser(create) error = %v", err)
		}
		if created.ID == "" || created.UserPath != "/team/alpha" || created.Name != "Alpha" {
			t.Fatalf("UpsertUser(create) = %+v", created)
		}
		if !reflect.DeepEqual(created.Groups, []string{"beta-testers"}) {
			t.Fatalf("UpsertUser(create).Groups = %v", created.Groups)
		}

		// Unknown group is rejected.
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/other", Groups: []string{"nope"}}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(unknown group) error = %v, want validation error", err)
		}

		// Duplicate path is rejected.
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/team/alpha/"}); !IsValidationError(err) {
			t.Fatalf("UpsertUser(duplicate path) error = %v, want validation error", err)
		}

		// Updating an unknown ID is rejected.
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{ID: "missing", UserPath: "/x"}); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("UpsertUser(unknown id) error = %v, want ErrUserNotFound", err)
		}

		// Update by ID can change the path and keeps CreatedAt.
		updated, err := svc.UpsertUser(ctx, UpsertUserInput{ID: created.ID, UserPath: "/team/renamed", Name: "Alpha"})
		if err != nil {
			t.Fatalf("UpsertUser(update) error = %v", err)
		}
		if updated.UserPath != "/team/renamed" || !updated.CreatedAt.Equal(created.CreatedAt) {
			t.Fatalf("UpsertUser(update) = %+v", updated)
		}
		if got := svc.ListUsers(); len(got) != 1 {
			t.Fatalf("ListUsers() len = %d, want 1", len(got))
		}

		if err := svc.DeleteUser(ctx, created.ID); err != nil {
			t.Fatalf("DeleteUser() error = %v", err)
		}
		if got := svc.ListUsers(); len(got) != 0 {
			t.Fatalf("ListUsers() after delete len = %d, want 0", len(got))
		}
	})
}

func TestServiceGroupsForPathUnionsAncestors(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()
		for _, name := range []string{"premium", "beta-testers", "ops"} {
			if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: name}); err != nil {
				t.Fatalf("UpsertGroup(%s) error = %v", name, err)
			}
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/team", Groups: []string{"premium"}}); err != nil {
			t.Fatalf("UpsertUser(/team) error = %v", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/team/alpha", Groups: []string{"beta-testers", "premium"}}); err != nil {
			t.Fatalf("UpsertUser(/team/alpha) error = %v", err)
		}

		// Deeper descendant unions every ancestor user's memberships.
		got := svc.GroupsForPath("/team/alpha/service")
		if want := []string{"beta-testers", "premium"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("GroupsForPath(/team/alpha/service) = %v, want %v", got, want)
		}
		if got := svc.GroupsForPath("/team"); !reflect.DeepEqual(got, []string{"premium"}) {
			t.Fatalf("GroupsForPath(/team) = %v", got)
		}
		if got := svc.GroupsForPath("/elsewhere"); got != nil {
			t.Fatalf("GroupsForPath(/elsewhere) = %v, want nil", got)
		}
		if got := svc.GroupsForPath(""); got != nil {
			t.Fatalf("GroupsForPath(empty) = %v, want nil", got)
		}
	})
}

func TestServiceDeleteGroupCascadesMemberships(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()
		for _, name := range []string{"premium", "beta-testers"} {
			if _, err := svc.UpsertGroup(ctx, UpsertGroupInput{Name: name}); err != nil {
				t.Fatalf("UpsertGroup(%s) error = %v", name, err)
			}
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/a", Groups: []string{"premium", "beta-testers"}}); err != nil {
			t.Fatalf("UpsertUser(/a) error = %v", err)
		}
		if _, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/b", Groups: []string{"beta-testers"}}); err != nil {
			t.Fatalf("UpsertUser(/b) error = %v", err)
		}

		if err := svc.DeleteGroup(ctx, "beta-testers"); err != nil {
			t.Fatalf("DeleteGroup() error = %v", err)
		}
		if got := svc.ListGroups(); len(got) != 1 || got[0].Name != "premium" {
			t.Fatalf("ListGroups() = %+v", got)
		}
		for _, user := range svc.ListUsers() {
			switch user.UserPath {
			case "/a":
				if !reflect.DeepEqual(user.Groups, []string{"premium"}) {
					t.Fatalf("user /a groups = %v, want [premium]", user.Groups)
				}
			case "/b":
				if user.Groups != nil {
					t.Fatalf("user /b groups = %v, want nil", user.Groups)
				}
			}
		}

		if err := svc.DeleteGroup(ctx, "beta-testers"); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("DeleteGroup(gone) error = %v, want ErrGroupNotFound", err)
		}
	})
}

func TestNormalizeGroupNames(t *testing.T) {
	t.Parallel()
	got, err := NormalizeGroupNames([]string{" b ", "a", "b", ""})
	if err != nil {
		t.Fatalf("NormalizeGroupNames() error = %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeGroupNames() = %v, want %v", got, want)
	}
	if _, err := NormalizeGroupNames([]string{"team/alpha"}); err == nil {
		t.Fatalf("NormalizeGroupNames(slash) error = nil, want validation error")
	}
	if _, err := NormalizeGroupNames([]string{"a,b"}); err == nil {
		t.Fatalf("NormalizeGroupNames(comma) error = nil, want validation error")
	}
}

func TestServiceUserByIDAndUserIDForPath(t *testing.T) {
	runServiceTest(t, func(t *testing.T, svc *Service) {
		ctx := context.Background()

		team, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/team/alpha", Name: "Alpha"})
		if err != nil {
			t.Fatalf("UpsertUser(team) error = %v", err)
		}
		service, err := svc.UpsertUser(ctx, UpsertUserInput{UserPath: "/team/alpha/service", Name: "Service"})
		if err != nil {
			t.Fatalf("UpsertUser(service) error = %v", err)
		}

		got, ok := svc.UserByID(team.ID)
		if !ok || got.UserPath != "/team/alpha" {
			t.Fatalf("UserByID(%q) = %+v, %v", team.ID, got, ok)
		}
		if _, ok := svc.UserByID("missing"); ok {
			t.Fatalf("UserByID(missing) = _, true, want false")
		}

		// Exact path match wins over the ancestor.
		if id := svc.UserIDForPath("/team/alpha/service"); id != service.ID {
			t.Fatalf("UserIDForPath(exact) = %q, want %q", id, service.ID)
		}
		// A descendant path attributes to its deepest registered ancestor.
		if id := svc.UserIDForPath("/team/alpha/service/worker"); id != service.ID {
			t.Fatalf("UserIDForPath(descendant) = %q, want %q", id, service.ID)
		}
		if id := svc.UserIDForPath("/team/alpha/other"); id != team.ID {
			t.Fatalf("UserIDForPath(sibling under team) = %q, want %q", id, team.ID)
		}
		// Unrelated and empty paths resolve to nobody.
		if id := svc.UserIDForPath("/sales"); id != "" {
			t.Fatalf("UserIDForPath(unrelated) = %q, want empty", id)
		}
		if id := svc.UserIDForPath(""); id != "" {
			t.Fatalf("UserIDForPath(empty) = %q, want empty", id)
		}
	})
}
