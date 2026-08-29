package users

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

const defaultRefreshInterval = time.Minute

type snapshot struct {
	userOrder  []string // user IDs sorted by user_path
	byID       map[string]User
	byPath     map[string]User
	groupOrder []string // group names sorted
	groups     map[string]Group
}

func emptySnapshot() snapshot {
	return snapshot{
		userOrder:  []string{},
		byID:       map[string]User{},
		byPath:     map[string]User{},
		groupOrder: []string{},
		groups:     map[string]Group{},
	}
}

// Service keeps users and groups cached in memory. Mutations write through the
// store and refresh the snapshot; a background refresh keeps replicas in sync.
type Service struct {
	store Store

	mu       sync.RWMutex
	snapshot snapshot
	writeMu  sync.Mutex
}

// NewService creates a user registry service backed by storage.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return &Service{store: store, snapshot: emptySnapshot()}, nil
}

// Refresh reloads users and groups from storage and atomically swaps the
// in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}

	next := snapshot{
		userOrder:  make([]string, 0, len(users)),
		byID:       make(map[string]User, len(users)),
		byPath:     make(map[string]User, len(users)),
		groupOrder: make([]string, 0, len(groups)),
		groups:     make(map[string]Group, len(groups)),
	}
	for _, user := range users {
		user.ID = normalizeID(user.ID)
		if user.ID == "" {
			return fmt.Errorf("load user %q: missing id", user.UserPath)
		}
		next.userOrder = append(next.userOrder, user.ID)
		next.byID[user.ID] = user
		next.byPath[user.UserPath] = user
	}
	sort.Slice(next.userOrder, func(i, j int) bool {
		left, right := next.byID[next.userOrder[i]], next.byID[next.userOrder[j]]
		if left.UserPath != right.UserPath {
			return left.UserPath < right.UserPath
		}
		return left.ID < right.ID
	})
	for _, group := range groups {
		next.groupOrder = append(next.groupOrder, group.Name)
		next.groups[group.Name] = group
	}
	sort.Strings(next.groupOrder)

	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// StartBackgroundRefresh keeps the snapshot in sync with storage so admin
// changes made by other replicas become visible. Returns a stop function.
func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var once sync.Once

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
				_ = s.Refresh(refreshCtx)
				refreshCancel()
			}
		}
	}()

	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (s *Service) current() snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// ListUsers returns every user sorted by user path.
func (s *Service) ListUsers() []User {
	if s == nil {
		return nil
	}
	snap := s.current()
	result := make([]User, 0, len(snap.userOrder))
	for _, id := range snap.userOrder {
		result = append(result, snap.byID[id].clone())
	}
	return result
}

// ListGroups returns every group sorted by name.
func (s *Service) ListGroups() []Group {
	if s == nil {
		return nil
	}
	snap := s.current()
	result := make([]Group, 0, len(snap.groupOrder))
	for _, name := range snap.groupOrder {
		result = append(result, snap.groups[name])
	}
	return result
}

// GroupsForPath resolves the groups carried by a user path: the union of the
// memberships of every user along the path's ancestor chain, so a user at
// /team/alpha passes its groups on to /team/alpha/service. The result is
// sorted and deduplicated; an unknown path yields nil.
func (s *Service) GroupsForPath(userPath string) []string {
	if s == nil {
		return nil
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return nil
	}
	snap := s.current()
	if len(snap.byPath) == 0 {
		return nil
	}

	var merged []string
	for _, ancestor := range core.UserPathAncestors(userPath) {
		user, ok := snap.byPath[ancestor]
		if !ok {
			continue
		}
		merged = append(merged, user.Groups...)
	}
	if len(merged) == 0 {
		return nil
	}
	sort.Strings(merged)
	return slices.Compact(merged)
}

// UserByID returns the user with the given id from the current snapshot.
func (s *Service) UserByID(id string) (User, bool) {
	if s == nil {
		return User{}, false
	}
	user, ok := s.current().byID[strings.TrimSpace(id)]
	if !ok {
		return User{}, false
	}
	return user.clone(), true
}

// UserIDForPath resolves the registered user that owns a request path: the
// user registered at the path itself, or at its deepest registered ancestor,
// so requests from /team/alpha/service attribute to the user at /team/alpha.
// Unknown paths yield "".
func (s *Service) UserIDForPath(userPath string) string {
	if s == nil {
		return ""
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return ""
	}
	snap := s.current()
	if len(snap.byPath) == 0 {
		return ""
	}
	for _, ancestor := range core.UserPathAncestors(userPath) {
		if user, ok := snap.byPath[ancestor]; ok {
			return user.ID
		}
	}
	return ""
}

// UpsertUser creates a new user (empty input ID) or updates an existing one.
func (s *Service) UpsertUser(ctx context.Context, input UpsertUserInput) (User, error) {
	if s == nil {
		return User{}, fmt.Errorf("users service is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	userPath, err := normalizeUserPath(input.UserPath)
	if err != nil {
		return User{}, err
	}
	groups, err := NormalizeGroupNames(input.Groups)
	if err != nil {
		return User{}, err
	}

	snap := s.current()
	for _, name := range groups {
		if _, ok := snap.groups[name]; !ok {
			return User{}, newValidationError("unknown group: "+name, nil)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	user := User{
		ID:          normalizeID(input.ID),
		UserPath:    userPath,
		Name:        trimmed(input.Name),
		Description: trimmed(input.Description),
		Groups:      groups,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if user.ID == "" {
		user.ID = uuid.NewString()
	} else {
		existing, ok := snap.byID[user.ID]
		if !ok {
			return User{}, ErrUserNotFound
		}
		user.CreatedAt = existing.CreatedAt
	}
	if other, ok := snap.byPath[userPath]; ok && other.ID != user.ID {
		return User{}, newValidationError("a user with user_path "+userPath+" already exists", nil)
	}

	if err := s.store.UpsertUser(ctx, user); err != nil {
		return User{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return User{}, err
	}
	return user.clone(), nil
}

// DeleteUser removes one user by ID.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	if s == nil {
		return fmt.Errorf("users service is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.store.DeleteUser(ctx, id); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// UpsertGroup creates or updates one group.
func (s *Service) UpsertGroup(ctx context.Context, input UpsertGroupInput) (Group, error) {
	if s == nil {
		return Group{}, fmt.Errorf("users service is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	name, err := NormalizeGroupName(input.Name)
	if err != nil {
		return Group{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	group := Group{
		Name:        name,
		Description: trimmed(input.Description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if existing, ok := s.current().groups[name]; ok {
		group.CreatedAt = existing.CreatedAt
	}

	if err := s.store.UpsertGroup(ctx, group); err != nil {
		return Group{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return Group{}, err
	}
	return group, nil
}

// DeleteGroup removes one group and cascades the membership out of every user
// that carries it. Access policies referencing the group keep the name but no
// request can carry it any more.
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	if s == nil {
		return fmt.Errorf("users service is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	name, err := NormalizeGroupName(name)
	if err != nil {
		return err
	}
	if err := s.store.DeleteGroup(ctx, name); err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Second)
	snap := s.current()
	for _, id := range snap.userOrder {
		user := snap.byID[id]
		if !slices.Contains(user.Groups, name) {
			continue
		}
		user = user.clone()
		user.Groups = slices.DeleteFunc(user.Groups, func(g string) bool { return g == name })
		if len(user.Groups) == 0 {
			user.Groups = nil
		}
		user.UpdatedAt = now
		if err := s.store.UpsertUser(ctx, user); err != nil {
			// Reload before surfacing the error so the snapshot reflects the
			// partially applied cascade.
			_ = s.Refresh(ctx)
			return err
		}
	}
	return s.Refresh(ctx)
}

func trimmed(value string) string {
	return strings.TrimSpace(value)
}
