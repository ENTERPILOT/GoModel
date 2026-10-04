package litellmmigrate

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/authkeys"
)

// ImportPlan lists what the database import creates in GoModel, in the order
// it is applied: model policies first, so no imported key is ever briefly
// less restricted than it was in LiteLLM.
type ImportPlan struct {
	Policies   []PolicyImport
	Budgets    []BudgetImport
	RateLimits []RateLimitImport
	Keys       []KeyImport
	// DisabledKeys counts keys that are blocked, expired, or in a blocked
	// team. They are sent disabled, so an earlier import is deactivated.
	DisabledKeys int
}

// PolicyImport is a user-path model allowlist (PUT /admin/users).
type PolicyImport struct {
	UserPath      string   `json:"user_path"`
	AllowedModels []string `json:"allowed_models"`
	Description   string   `json:"description,omitempty"`
}

// BudgetImport is a spend limit on a user path (PUT /admin/budgets).
type BudgetImport struct {
	UserPath      string
	Period        string
	PeriodSeconds int64
	Amount        float64
	// Spend is what LiteLLM counted in its current period. GoModel starts the
	// period from its own usage records.
	Spend float64
}

// RateLimitImport is a rate limit rule on a user path (PUT /admin/rate-limits).
type RateLimitImport struct {
	UserPath    string
	Period      string
	MaxRequests *int64
	MaxTokens   *int64
}

// KeyImport is one LiteLLM virtual key (POST /admin/auth-keys/import).
type KeyImport struct {
	Name          string     `json:"name"`
	ImportedFrom  string     `json:"imported_from"`
	SecretHash    string     `json:"secret_hash"`
	RedactedValue string     `json:"redacted_value,omitempty"`
	UserPath      string     `json:"user_path"`
	Labels        []string   `json:"labels,omitempty"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	// Enabled is false for a key LiteLLM blocked or expired.
	Enabled *bool `json:"enabled,omitempty"`
}

// LiteLLM's special model list entries.
const (
	allProxyModels  = "all-proxy-models"
	allTeamModels   = "all-team-models"
	noDefaultModels = "no-default-models"
)

type planner struct {
	db      *Database
	result  *Result
	report  *Report
	now     time.Time
	plan    ImportPlan
	targets map[string][]string
	known   map[string]bool
	unknown map[string]bool
	budgets map[string]dbBudget
	orgs    map[string]dbOrganization
	teams   map[string]dbTeam
	users   map[string]dbUser
	paths   map[string]string
	used    map[string]bool
}

// PlanImport maps the LiteLLM database onto GoModel user paths, model
// policies, budgets, rate limits, and imported keys, and records what does
// not carry over in result's report. Model names resolve through the virtual
// models the config conversion produced.
func PlanImport(db *Database, result *Result, now time.Time) *ImportPlan {
	p := &planner{
		db:      db,
		result:  result,
		report:  result.Report,
		now:     now.UTC(),
		targets: map[string][]string{},
		known:   map[string]bool{},
		unknown: map[string]bool{},
		budgets: map[string]dbBudget{},
		orgs:    map[string]dbOrganization{},
		teams:   map[string]dbTeam{},
		users:   map[string]dbUser{},
		paths:   map[string]string{},
		used:    map[string]bool{},
	}
	p.index()
	// The groups personal and unassigned keys live under; an organization or
	// team with one of these names gets a suffix instead.
	p.used["/users"], p.used["/keys"] = true, true
	// Rows come in no fixed order. Oldest first keeps the paths of objects
	// sharing a name stable across runs as new ones are added.
	sortByCreation(db.Organizations, func(o dbOrganization) (*dbTime, string) { return o.CreatedAt, o.OrganizationID })
	sortByCreation(db.Teams, func(t dbTeam) (*dbTime, string) { return t.CreatedAt, t.TeamID })
	sortByCreation(db.Users, func(u dbUser) (*dbTime, string) { return u.CreatedAt, u.UserID })
	sortByCreation(db.Keys, func(k dbKey) (*dbTime, string) { return k.CreatedAt, k.Token })
	for _, org := range db.Organizations {
		p.addOrganization(org)
	}
	for _, team := range db.Teams {
		p.addTeam(team)
	}
	for _, user := range db.Users {
		p.userPath(user.UserID)
	}
	for _, key := range db.Keys {
		p.addKey(key)
	}
	for name := range p.unknown {
		p.report.warn("models "+name, "named by LiteLLM keys, teams, or users but not a model_name in the LiteLLM config; access to it stays closed until it exists in GoModel")
	}
	result.Report.Import = &p.plan
	return &p.plan
}

func (p *planner) index() {
	for _, vm := range p.result.Report.VirtualModels {
		p.known[vm.Source] = true
		for _, t := range vm.Targets {
			p.targets[vm.Source] = append(p.targets[vm.Source], t.qualified())
		}
	}
	for _, provider := range p.result.Report.Providers {
		for _, name := range provider.ModelNames {
			p.known[name] = true
		}
	}
	for _, b := range p.db.Budgets {
		p.budgets[b.BudgetID] = b
	}
	for _, org := range p.db.Organizations {
		p.orgs[org.OrganizationID] = org
	}
	for _, team := range p.db.Teams {
		p.teams[team.TeamID] = team
	}
	for _, user := range p.db.Users {
		p.users[user.UserID] = user
	}
}

func (p *planner) addOrganization(org dbOrganization) {
	path := p.orgPath(org.OrganizationID)
	limits := dbLimits{}
	if org.BudgetID != nil {
		limits = p.budgets[*org.BudgetID].dbLimits
	}
	p.addPolicy(path, org.Models, "organization "+label(org.OrganizationAlias, org.OrganizationID))
	p.addLimits(path, limits, org.Spend, "organization "+label(org.OrganizationAlias, org.OrganizationID))
}

func (p *planner) addTeam(team dbTeam) {
	subject := "team " + label(team.TeamAlias, team.TeamID)
	path := p.teamPath(team.TeamID)
	if isTrue(team.Blocked) {
		p.report.skip(subject, "blocked in LiteLLM; its keys are imported deactivated")
		return
	}
	p.addPolicy(path, team.Models, subject)
	p.addLimits(path, team.dbLimits, team.Spend, subject)
}

func (p *planner) addKey(key dbKey) {
	name := label(key.KeyAlias, label(key.KeyName, "litellm-key"))
	subject := "key " + name
	disabled := ""
	switch {
	case isTrue(key.Blocked):
		disabled = "blocked in LiteLLM"
	case key.Expires != nil && !key.Expires.After(p.now):
		disabled = "expired in LiteLLM"
	case key.TeamID != nil && isTrue(p.teams[*key.TeamID].Blocked):
		disabled = "its team is blocked in LiteLLM"
	}
	var parent string
	switch {
	case key.TeamID != nil && *key.TeamID != "":
		parent = p.teamPath(*key.TeamID)
	case key.UserID != nil && *key.UserID != "":
		parent = p.userPath(*key.UserID)
	default:
		parent = "/keys"
	}
	path := p.unique(parent+"/"+segment(name, shortID(key.Token)), shortID(key.Token))
	if disabled != "" {
		p.report.skip(subject, disabled+"; deactivated in GoModel if it was imported before, not created otherwise")
		p.plan.DisabledKeys++
		p.plan.Keys = append(p.plan.Keys, KeyImport{
			Name:          name,
			ImportedFrom:  authkeys.ImportedFromLiteLLM,
			SecretHash:    key.Token,
			RedactedValue: redactedValue(key.KeyName),
			UserPath:      path,
			Enabled:       new(false),
		})
		return
	}

	limits := key.dbLimits
	if key.BudgetID != nil {
		limits = mergeLimits(limits, p.budgets[*key.BudgetID].dbLimits)
	}
	p.addLimits(path, limits, key.Spend, subject)

	item := KeyImport{
		Name:          name,
		ImportedFrom:  authkeys.ImportedFromLiteLLM,
		SecretHash:    key.Token,
		RedactedValue: redactedValue(key.KeyName),
		UserPath:      path,
		Labels:        tags(key.Metadata),
	}
	if key.TeamID != nil {
		item.Labels = mergeTags(item.Labels, tags(p.teams[*key.TeamID].Metadata))
	}
	if models, restricted := p.allowedModels(key.Models, true); restricted {
		item.AllowedModels = models
	}
	if key.Expires != nil {
		expires := key.Expires.Time
		item.ExpiresAt = &expires
	}
	p.plan.Keys = append(p.plan.Keys, item)
}

// addPolicy restricts path to models, unless the list leaves it unrestricted.
func (p *planner) addPolicy(path string, models []string, subject string) {
	allowed, restricted := p.allowedModels(models, false)
	if !restricted {
		return
	}
	p.plan.Policies = append(p.plan.Policies, PolicyImport{
		UserPath:      path,
		AllowedModels: allowed,
		Description:   "Migrated from LiteLLM " + subject,
	})
}

// allowedModels resolves a LiteLLM model list to GoModel selectors. An empty
// list, all-proxy-models, and on a key all-team-models leave access
// unrestricted at that level. Names of virtual models resolve to the models
// they route to, because GoModel checks the model a request resolves to.
// Other names, including no-default-models, are kept: they match nothing, so
// access stays closed as it was in LiteLLM.
func (p *planner) allowedModels(models []string, onKey bool) ([]string, bool) {
	if len(models) == 0 || slices.Contains(models, allProxyModels) || (onKey && slices.Contains(models, allTeamModels)) {
		return nil, false
	}
	var out []string
	for _, name := range models {
		if targets := p.resolve(name, 0); len(targets) > 0 {
			out = append(out, targets...)
			continue
		}
		if !p.known[name] && name != noDefaultModels && name != allTeamModels {
			p.unknown[name] = true
		}
		out = append(out, name)
	}
	return dedupe(out), true
}

// resolve returns the provider models behind a virtual model, descending
// into virtual models it targets, such as a fallback's load balancer.
func (p *planner) resolve(name string, depth int) []string {
	targets, ok := p.targets[name]
	if !ok || depth > 8 {
		return nil
	}
	var out []string
	for _, target := range targets {
		if inner := p.resolve(target, depth+1); len(inner) > 0 {
			out = append(out, inner...)
			continue
		}
		out = append(out, target)
	}
	return out
}

func (p *planner) addLimits(path string, limits dbLimits, spend float64, subject string) {
	if limits.MaxBudget != nil {
		p.addBudget(path, *limits.MaxBudget, deref(limits.BudgetDuration), spend, subject)
	}
	if limits.RPMLimit != nil || limits.TPMLimit != nil {
		p.plan.RateLimits = append(p.plan.RateLimits, RateLimitImport{UserPath: path, Period: "minute", MaxRequests: limits.RPMLimit, MaxTokens: limits.TPMLimit})
	}
	if limits.TPDLimit != nil {
		p.plan.RateLimits = append(p.plan.RateLimits, RateLimitImport{UserPath: path, Period: "day", MaxTokens: limits.TPDLimit})
	}
	if limits.MaxParallelRequests != nil {
		p.plan.RateLimits = append(p.plan.RateLimits, RateLimitImport{UserPath: path, Period: "concurrent", MaxRequests: limits.MaxParallelRequests})
	}
}

func (p *planner) addBudget(path string, amount float64, duration string, spend float64, subject string) {
	if duration == "" {
		p.report.skip(subject+" max_budget", fmt.Sprintf("$%g with no budget_duration never resets; GoModel budgets reset every period. Add one on %s if you need it", amount, path))
		return
	}
	seconds, months, ok := parseBudgetDuration(duration)
	if !ok {
		p.report.skip(subject+" budget_duration", fmt.Sprintf("%q is not a duration GoModel understands; the $%g budget was not migrated", duration, amount))
		return
	}
	item := BudgetImport{UserPath: path, Amount: amount, Spend: spend}
	switch {
	case months == 1:
		item.Period = "monthly"
	case seconds == 3600:
		item.Period = "hourly"
	case seconds == 86400:
		item.Period = "daily"
	case seconds == 7*86400:
		item.Period = "weekly"
	case seconds == 30*86400:
		item.Period = "monthly"
		p.report.info(subject+" budget_duration", "30d became a monthly budget, which resets on calendar months")
	case months > 1:
		item.PeriodSeconds = int64(months) * 30 * 86400
		p.report.info(subject+" budget_duration", fmt.Sprintf("%s became a fixed %d-day window", duration, months*30))
	default:
		item.PeriodSeconds = seconds
	}
	p.plan.Budgets = append(p.plan.Budgets, item)
}

// parseBudgetDuration reads LiteLLM's budget_duration, such as "30s", "12h",
// "7d", "2w", or "1mo". months is set for calendar-month durations.
func parseBudgetDuration(value string) (seconds int64, months int, ok bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	units := []struct {
		suffix  string
		seconds int64
	}{{"mo", 0}, {"s", 1}, {"m", 60}, {"h", 3600}, {"d", 86400}, {"w", 7 * 86400}}
	for _, unit := range units {
		number, found := strings.CutSuffix(value, unit.suffix)
		if !found {
			continue
		}
		n, err := strconv.Atoi(number)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if unit.suffix == "mo" {
			return 0, n, true
		}
		return int64(n) * unit.seconds, 0, true
	}
	return 0, 0, false
}

func (p *planner) orgPath(id string) string {
	return p.pathFor("org:"+id, "/"+segment(label(p.orgs[id].OrganizationAlias, id), id), shortID(id))
}

func (p *planner) teamPath(id string) string {
	team := p.teams[id]
	parent := ""
	if team.OrganizationID != nil && *team.OrganizationID != "" {
		parent = p.orgPath(*team.OrganizationID)
	}
	return p.pathFor("team:"+id, parent+"/"+segment(label(team.TeamAlias, id), id), shortID(id))
}

// userPath is a personal key's parent. The user's own model list, budget,
// and limits sit on it, so they bind the user's personal keys and not the
// team keys the user holds, as in LiteLLM.
func (p *planner) userPath(id string) string {
	if path, ok := p.paths["user:"+id]; ok {
		return path
	}
	user, ok := p.users[id]
	name := id
	if ok {
		name = label(user.UserAlias, label(user.UserEmail, id))
	}
	path := p.pathFor("user:"+id, "/users/"+segment(name, id), shortID(id))
	if ok {
		subject := "user " + name
		p.addPolicy(path, user.Models, subject)
		p.addLimits(path, user.dbLimits, user.Spend, subject)
	}
	return path
}

// pathFor returns the path assigned to entity, assigning want (made unique)
// the first time.
func (p *planner) pathFor(entity, want, id string) string {
	if path, ok := p.paths[entity]; ok {
		return path
	}
	path := p.unique(want, id)
	p.paths[entity] = path
	return path
}

// unique returns path, or when it is taken, path with a suffix from the
// object's own id, so the suffix does not depend on which other objects exist.
func (p *planner) unique(path, id string) string {
	candidate := path
	if p.used[candidate] {
		candidate = path + "-" + id
	}
	for n := 2; p.used[candidate]; n++ {
		candidate = path + "-" + id + "-" + strconv.Itoa(n)
	}
	p.used[candidate] = true
	return candidate
}

// shortID is the start of a LiteLLM id or key hash, enough to tell objects
// that share a name apart.
func shortID(id string) string {
	return segment(id[:min(8, len(id))], "id")
}

func sortByCreation[T any](items []T, key func(T) (*dbTime, string)) {
	slices.SortStableFunc(items, func(a, b T) int {
		createdA, idA := key(a)
		createdB, idB := key(b)
		switch {
		case createdA != nil && createdB != nil && !createdA.Equal(createdB.Time):
			return createdA.Compare(createdB.Time)
		case (createdA == nil) != (createdB == nil):
			// Rows without a timestamp go last.
			if createdA == nil {
				return 1
			}
			return -1
		}
		return strings.Compare(idA, idB)
	})
}

// segment makes name one user path segment: "/" and ":" become "-", and a
// name that is empty or a dot segment falls back to id.
func segment(name, id string) string {
	clean := strings.TrimSpace(strings.NewReplacer("/", "-", ":", "-").Replace(name))
	if clean == "" || clean == "." || clean == ".." {
		clean = strings.TrimSpace(strings.NewReplacer("/", "-", ":", "-").Replace(id))
	}
	if clean == "" || clean == "." || clean == ".." {
		return "unnamed"
	}
	return clean
}

func mergeLimits(own, fallback dbLimits) dbLimits {
	if own.MaxBudget == nil {
		own.MaxBudget = fallback.MaxBudget
	}
	if own.BudgetDuration == nil {
		own.BudgetDuration = fallback.BudgetDuration
	}
	if own.TPMLimit == nil {
		own.TPMLimit = fallback.TPMLimit
	}
	if own.RPMLimit == nil {
		own.RPMLimit = fallback.RPMLimit
	}
	if own.TPDLimit == nil {
		own.TPDLimit = fallback.TPDLimit
	}
	if own.MaxParallelRequests == nil {
		own.MaxParallelRequests = fallback.MaxParallelRequests
	}
	return own
}

func tags(metadata map[string]any) []string {
	raw, _ := metadata["tags"].([]any)
	var out []string
	for _, item := range raw {
		if tag, ok := item.(string); ok && strings.TrimSpace(tag) != "" {
			out = append(out, strings.TrimSpace(tag))
		}
	}
	return out
}

func mergeTags(a, b []string) []string { return dedupe(append(a, b...)) }

func dedupe(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// redactedValue returns LiteLLM's abbreviated key_name, such as "sk-...abcd",
// or "" when it has another shape, which GoModel would reject.
func redactedValue(keyName *string) string {
	rest, ok := strings.CutPrefix(deref(keyName), "sk-...")
	if !ok || len(rest) > 8 || strings.ContainsAny(rest, " \t\n") {
		return ""
	}
	return "sk-..." + rest
}

func label(preferred *string, fallback string) string {
	if preferred != nil && strings.TrimSpace(*preferred) != "" {
		return strings.TrimSpace(*preferred)
	}
	return fallback
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isTrue(value *bool) bool { return value != nil && *value }
