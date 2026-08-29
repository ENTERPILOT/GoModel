package users

import (
	"context"
	"fmt"
	"maps"
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
	userOrder   []string // user IDs sorted by user_path
	byID        map[string]User
	byPath      map[string]User
	groupOrder  []string         // group names sorted
	groups      map[string]Group // Path filled from the tree
	groupByPath map[string]string
}

func emptySnapshot() snapshot {
	return snapshot{
		userOrder:   []string{},
		byID:        map[string]User{},
		byPath:      map[string]User{},
		groupOrder:  []string{},
		groups:      map[string]Group{},
		groupByPath: map[string]string{},
	}
}

// computeGroupPaths derives every group's hierarchy path from the parent
// tree. A missing parent or a cycle degrades to treating the group as a root
// rather than failing the load.
func computeGroupPaths(groups map[string]Group) map[string]string {
	paths := make(map[string]string, len(groups))
	var resolve func(name string, trail map[string]bool) string
	resolve = func(name string, trail map[string]bool) string {
		if path, done := paths[name]; done {
			return path
		}
		group, ok := groups[name]
		if !ok {
			return ""
		}
		path := "/" + group.Name
		if group.Parent != "" && !trail[group.Parent] {
			trail[name] = true
			if parentPath := resolve(group.Parent, trail); parentPath != "" {
				path = parentPath + "/" + group.Name
			}
		}
		paths[name] = path
		return path
	}
	for name := range groups {
		resolve(name, map[string]bool{name: true})
	}
	return paths
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
		userOrder:   make([]string, 0, len(users)),
		byID:        make(map[string]User, len(users)),
		byPath:      make(map[string]User, len(users)),
		groupOrder:  make([]string, 0, len(groups)),
		groups:      make(map[string]Group, len(groups)),
		groupByPath: make(map[string]string, len(groups)),
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
	for name, path := range computeGroupPaths(next.groups) {
		group := next.groups[name]
		group.Path = path
		next.groups[name] = group
		next.groupByPath[path] = name
	}

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
		result = append(result, snap.byID[id])
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

// GroupsForPath resolves the groups carried by a user path. Because user
// paths mirror the group tree, membership is path-prefix matching: the caller
// carries every group whose derived path is the caller path or one of its
// ancestors, so a request under /engineering/platform/anna carries both
// "platform" and "engineering". The result is sorted; an unknown path yields
// nil.
func (s *Service) GroupsForPath(userPath string) []string {
	if s == nil {
		return nil
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return nil
	}
	snap := s.current()
	if len(snap.groupByPath) == 0 {
		return nil
	}

	var merged []string
	for _, ancestor := range core.UserPathAncestors(userPath) {
		if name, ok := snap.groupByPath[ancestor]; ok {
			merged = append(merged, name)
		}
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
	return user, true
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

	name, err := normalizeUserName(input.Name)
	if err != nil {
		return User{}, err
	}
	groupName := trimmed(input.Group)

	snap := s.current()
	groupPath := ""
	if groupName != "" {
		group, ok := snap.groups[groupName]
		if !ok {
			return User{}, newValidationError("unknown group: "+groupName, nil)
		}
		groupPath = group.Path
	}
	userPath, err := derivedPath(groupPath, name)
	if err != nil {
		return User{}, err
	}

	now := time.Now().UTC().Truncate(time.Second)
	user := User{
		ID:          normalizeID(input.ID),
		UserPath:    userPath,
		Name:        name,
		Description: trimmed(input.Description),
		Group:       groupName,
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
		return User{}, newValidationError("a user at "+userPath+" already exists", nil)
	}
	if _, ok := snap.groupByPath[userPath]; ok {
		return User{}, newValidationError("a group already owns the path "+userPath, nil)
	}

	if err := s.store.UpsertUser(ctx, user); err != nil {
		return User{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return User{}, err
	}
	return user, nil
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
	parent := trimmed(input.Parent)

	snap := s.current()
	if parent != "" {
		if parent == name {
			return Group{}, newValidationError("a group cannot be its own parent", nil)
		}
		if _, ok := snap.groups[parent]; !ok {
			return Group{}, newValidationError("unknown parent group: "+parent, nil)
		}
		// Reject a parent that sits below this group: the chain from the new
		// parent to the root must not pass through the group itself.
		for cursor := parent; cursor != ""; {
			group, ok := snap.groups[cursor]
			if !ok {
				break
			}
			if group.Parent == name {
				return Group{}, newValidationError("cannot move group under its own descendant "+parent, nil)
			}
			cursor = group.Parent
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	group := Group{
		Name:        name,
		Description: trimmed(input.Description),
		Parent:      parent,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if existing, ok := snap.groups[name]; ok {
		group.CreatedAt = existing.CreatedAt
	}

	// Derive every path against the prospective tree so a move that would
	// collide is rejected before anything is written.
	nextGroups := make(map[string]Group, len(snap.groups)+1)
	maps.Copy(nextGroups, snap.groups)
	nextGroups[name] = group
	rewrites, err := planUserPathRewrites(snap, nextGroups)
	if err != nil {
		return Group{}, err
	}

	if err := s.store.UpsertGroup(ctx, group); err != nil {
		return Group{}, err
	}
	if err := s.applyUserPathRewrites(ctx, rewrites, now); err != nil {
		return Group{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return Group{}, err
	}
	if refreshed, ok := s.current().groups[name]; ok {
		return refreshed, nil
	}
	return group, nil
}

// planUserPathRewrites derives every user's path against a prospective group
// tree and returns the users whose stored path changes. It fails when two
// derived paths collide or a derived path collides with a group path.
func planUserPathRewrites(snap snapshot, nextGroups map[string]Group) ([]User, error) {
	paths := computeGroupPaths(nextGroups)
	groupPathSet := make(map[string]bool, len(paths))
	for _, path := range paths {
		groupPathSet[path] = true
	}

	var rewrites []User
	seen := make(map[string]string, len(snap.userOrder))
	for _, id := range snap.userOrder {
		user := snap.byID[id]
		parentPath := ""
		if user.Group != "" {
			// A user whose group is gone keeps its stored path; deletion is
			// guarded separately, so this only covers out-of-band drift.
			path, ok := paths[user.Group]
			if !ok {
				continue
			}
			parentPath = path
		}
		derived, err := derivedPath(parentPath, user.Name)
		if err != nil {
			return nil, err
		}
		if owner, dup := seen[derived]; dup {
			return nil, newValidationError("the move would collide user paths at "+derived+" (users "+owner+" and "+user.ID+")", nil)
		}
		seen[derived] = user.ID
		if groupPathSet[derived] {
			return nil, newValidationError("the move would collide user and group paths at "+derived, nil)
		}
		if derived != user.UserPath {
			user.UserPath = derived
			rewrites = append(rewrites, user)
		}
	}
	return rewrites, nil
}

// applyUserPathRewrites writes through the users whose derived path changed.
func (s *Service) applyUserPathRewrites(ctx context.Context, rewrites []User, now time.Time) error {
	for _, user := range rewrites {
		user.UpdatedAt = now
		if err := s.store.UpsertUser(ctx, user); err != nil {
			// Reload before surfacing the error so the snapshot reflects the
			// partially applied cascade.
			_ = s.Refresh(ctx)
			return err
		}
	}
	return nil
}

// DeleteGroup removes one empty group. A group that still has member users or
// child groups is refused: deleting it would silently rewrite their derived
// paths, so the members must be moved or deleted first.
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
	snap := s.current()
	for _, groupName := range snap.groupOrder {
		if snap.groups[groupName].Parent == name {
			return newValidationError("group "+name+" still has subgroup "+groupName+"; move or delete it first", nil)
		}
	}
	for _, id := range snap.userOrder {
		if snap.byID[id].Group == name {
			return newValidationError("group "+name+" still has members; move or delete them first", nil)
		}
	}
	if err := s.store.DeleteGroup(ctx, name); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func trimmed(value string) string {
	return strings.TrimSpace(value)
}
