package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/users"
)

type usersTestStore struct {
	users  map[string]users.User
	groups map[string]users.Group
}

func newUsersTestStore() *usersTestStore {
	return &usersTestStore{users: map[string]users.User{}, groups: map[string]users.Group{}}
}

func (s *usersTestStore) ListUsers(_ context.Context) ([]users.User, error) {
	result := make([]users.User, 0, len(s.users))
	for _, user := range s.users {
		result = append(result, user)
	}
	return result, nil
}

func (s *usersTestStore) UpsertUser(_ context.Context, user users.User) error {
	s.users[user.ID] = user
	return nil
}

func (s *usersTestStore) DeleteUser(_ context.Context, id string) error {
	if _, ok := s.users[id]; !ok {
		return users.ErrUserNotFound
	}
	delete(s.users, id)
	return nil
}

func (s *usersTestStore) ListGroups(_ context.Context) ([]users.Group, error) {
	result := make([]users.Group, 0, len(s.groups))
	for _, group := range s.groups {
		result = append(result, group)
	}
	return result, nil
}

func (s *usersTestStore) UpsertGroup(_ context.Context, group users.Group) error {
	s.groups[group.Name] = group
	return nil
}

func (s *usersTestStore) DeleteGroup(_ context.Context, name string) error {
	if _, ok := s.groups[name]; !ok {
		return users.ErrGroupNotFound
	}
	delete(s.groups, name)
	return nil
}

func (s *usersTestStore) Close() error { return nil }

func newUsersHandler(t *testing.T) *Handler {
	t.Helper()
	service, err := users.NewService(newUsersTestStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return NewHandler(nil, nil, WithUsers(service))
}

func usersJSONRequest(t *testing.T, h func(*echo.Context) error, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	var reader *bytes.Buffer
	if body == "" {
		reader = bytes.NewBufferString("{}")
	} else {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	if err := h(ctx); err != nil {
		t.Fatalf("%s %s error = %v", method, path, err)
	}
	return rec
}

func TestUserEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	for name, call := range map[string]func(*echo.Context) error{
		"ListUsers":       h.ListUsers,
		"UpsertUser":      h.UpsertUser,
		"DeleteUser":      h.DeleteUser,
		"ListUserGroups":  h.ListUserGroups,
		"UpsertUserGroup": h.UpsertUserGroup,
		"DeleteUserGroup": h.DeleteUserGroup,
	} {
		rec := usersJSONRequest(t, call, http.MethodPost, "/admin/users", "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", name, rec.Code)
		}
	}
}

func TestUserAndGroupCRUDFlow(t *testing.T) {
	h := newUsersHandler(t)

	// Create a group first so the user can join it.
	rec := usersJSONRequest(t, h.UpsertUserGroup, http.MethodPut, "/admin/user-groups",
		`{"name":"beta-testers","description":"early access"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("UpsertUserGroup status = %d body=%s", rec.Code, rec.Body.String())
	}

	// User with an unknown group is rejected.
	rec = usersJSONRequest(t, h.UpsertUser, http.MethodPut, "/admin/users",
		`{"user_path":"/team/alpha","groups":["nope"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("UpsertUser(unknown group) status = %d, want 400", rec.Code)
	}

	// Create a user.
	rec = usersJSONRequest(t, h.UpsertUser, http.MethodPut, "/admin/users",
		`{"user_path":"team/alpha","name":"Alpha","groups":["beta-testers"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("UpsertUser status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created users.User
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}
	if created.ID == "" || created.UserPath != "/team/alpha" || !slices.Contains(created.Groups, "beta-testers") {
		t.Fatalf("created user = %+v", created)
	}

	// Missing user_path is a validation error.
	rec = usersJSONRequest(t, h.UpsertUser, http.MethodPut, "/admin/users", `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("UpsertUser(no path) status = %d, want 400", rec.Code)
	}

	// List includes the created user.
	rec = usersJSONRequest(t, h.ListUsers, http.MethodGet, "/admin/users", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ListUsers status = %d", rec.Code)
	}
	var listed []users.User
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode users list: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListUsers = %+v", listed)
	}

	// Deleting the group cascades the membership away.
	rec = usersJSONRequest(t, h.DeleteUserGroup, http.MethodDelete, "/admin/user-groups", `{"name":"beta-testers"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DeleteUserGroup status = %d", rec.Code)
	}
	rec = usersJSONRequest(t, h.ListUsers, http.MethodGet, "/admin/users", "")
	listed = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode users list: %v", err)
	}
	if len(listed) != 1 || len(listed[0].Groups) != 0 {
		t.Fatalf("ListUsers after group delete = %+v", listed)
	}

	// Delete the user; a second delete is a 404.
	rec = usersJSONRequest(t, h.DeleteUser, http.MethodDelete, "/admin/users", `{"id":"`+created.ID+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DeleteUser status = %d", rec.Code)
	}
	rec = usersJSONRequest(t, h.DeleteUser, http.MethodDelete, "/admin/users", `{"id":"`+created.ID+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DeleteUser(gone) status = %d, want 404", rec.Code)
	}
}
