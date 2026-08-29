package authkeys

import (
	"context"
	"errors"
	"testing"
)

func newUserBoundService(t *testing.T, users map[string]string) *Service {
	t.Helper()
	service, err := NewService(newTestStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.SetUserResolver(func(id string) (string, bool) {
		path, ok := users[id]
		return path, ok
	})
	return service
}

func TestServiceCreateUserBoundKeySnapshotsPath(t *testing.T) {
	users := map[string]string{"user-1": "/team/alpha"}
	service := newUserBoundService(t, users)

	issued, err := service.Create(context.Background(), CreateInput{Name: "bound", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.UserID != "user-1" {
		t.Fatalf("issued.UserID = %q, want user-1", issued.UserID)
	}
	if issued.UserPath != "/team/alpha" {
		t.Fatalf("issued.UserPath = %q, want snapshot /team/alpha", issued.UserPath)
	}
}

func TestServiceAuthenticateResolvesCurrentUserPath(t *testing.T) {
	users := map[string]string{"user-1": "/team/alpha"}
	service := newUserBoundService(t, users)

	issued, err := service.Create(context.Background(), CreateInput{Name: "bound", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// The user moves in the hierarchy; the key follows without any update.
	users["user-1"] = "/org/beta"

	result, err := service.Authenticate(context.Background(), issued.Value)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if result.UserPath != "/org/beta" {
		t.Fatalf("Authenticate().UserPath = %q, want live path /org/beta", result.UserPath)
	}
	if result.UserID != "user-1" {
		t.Fatalf("Authenticate().UserID = %q, want user-1", result.UserID)
	}

	// The list view shows the live path too.
	views := service.ListViews()
	if len(views) != 1 || views[0].UserPath != "/org/beta" {
		t.Fatalf("ListViews() user path = %v, want /org/beta", views)
	}
}

func TestServiceAuthenticateFallsBackToSnapshotPath(t *testing.T) {
	users := map[string]string{"user-1": "/team/alpha"}
	service := newUserBoundService(t, users)

	issued, err := service.Create(context.Background(), CreateInput{Name: "bound", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// User deleted from the registry: the key keeps its creation-time scope.
	delete(users, "user-1")

	result, err := service.Authenticate(context.Background(), issued.Value)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if result.UserPath != "/team/alpha" {
		t.Fatalf("Authenticate().UserPath = %q, want snapshot /team/alpha", result.UserPath)
	}
}

func TestServiceCreateRejectsUnknownUser(t *testing.T) {
	service := newUserBoundService(t, map[string]string{})
	if _, err := service.Create(context.Background(), CreateInput{Name: "bound", UserID: "ghost"}); !IsValidationError(err) {
		t.Fatalf("Create(unknown user) error = %v, want validation error", err)
	}
}

func TestServiceCreateRejectsUserIDWithUserPath(t *testing.T) {
	service := newUserBoundService(t, map[string]string{"user-1": "/team/alpha"})
	input := CreateInput{Name: "bound", UserID: "user-1", UserPath: "/other"}
	if _, err := service.Create(context.Background(), input); !IsValidationError(err) {
		t.Fatalf("Create(user_id+user_path) error = %v, want validation error", err)
	}
}

func TestServiceCreateUserBoundKeyWithoutResolverRejected(t *testing.T) {
	service, err := NewService(newTestStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := service.Create(context.Background(), CreateInput{Name: "bound", UserID: "user-1"}); !IsValidationError(err) {
		t.Fatalf("Create(no resolver) error = %v, want validation error", err)
	}
}

func TestServiceUpdateUserBindingBindsExistingKey(t *testing.T) {
	users := map[string]string{"user-1": "/team/alpha"}
	service := newUserBoundService(t, users)

	issued, err := service.Create(context.Background(), CreateInput{Name: "plain", UserPath: "/legacy"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	view, err := service.UpdateUserBinding(context.Background(), issued.ID, "user-1")
	if err != nil {
		t.Fatalf("UpdateUserBinding() error = %v", err)
	}
	if view.UserID != "user-1" || view.UserPath != "/team/alpha" {
		t.Fatalf("bound view = %+v, want user-1 at /team/alpha", view.AuthKey)
	}

	// The bound key now authenticates with the user's live path.
	users["user-1"] = "/org/beta"
	result, err := service.Authenticate(context.Background(), issued.Value)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if result.UserPath != "/org/beta" || result.UserID != "user-1" {
		t.Fatalf("Authenticate() = %+v, want live /org/beta for user-1", result)
	}
}

func TestServiceUpdateUserBindingUnbindFreezesCurrentPath(t *testing.T) {
	users := map[string]string{"user-1": "/team/alpha"}
	service := newUserBoundService(t, users)

	issued, err := service.Create(context.Background(), CreateInput{Name: "bound", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// The user moved after creation; unbinding freezes the key at the
	// current path, not the creation-time snapshot.
	users["user-1"] = "/org/beta"
	view, err := service.UpdateUserBinding(context.Background(), issued.ID, "")
	if err != nil {
		t.Fatalf("UpdateUserBinding(unbind) error = %v", err)
	}
	if view.UserID != "" || view.UserPath != "/org/beta" {
		t.Fatalf("unbound view = %+v, want frozen path /org/beta", view.AuthKey)
	}

	// Later user moves no longer affect the key.
	users["user-1"] = "/elsewhere"
	result, err := service.Authenticate(context.Background(), issued.Value)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if result.UserPath != "/org/beta" {
		t.Fatalf("Authenticate().UserPath = %q, want frozen /org/beta", result.UserPath)
	}
}

func TestServiceUpdateUserBindingRejectsUnknownUser(t *testing.T) {
	service := newUserBoundService(t, map[string]string{})

	issued, err := service.Create(context.Background(), CreateInput{Name: "plain"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.UpdateUserBinding(context.Background(), issued.ID, "ghost"); !IsValidationError(err) {
		t.Fatalf("UpdateUserBinding(unknown user) error = %v, want validation error", err)
	}
}

func TestServiceUpdateUserBindingUnknownKeyReturnsNotFound(t *testing.T) {
	service := newUserBoundService(t, map[string]string{"user-1": "/team/alpha"})
	if _, err := service.UpdateUserBinding(context.Background(), "missing", "user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateUserBinding(missing key) error = %v, want ErrNotFound", err)
	}
}
