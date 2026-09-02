// Package versioncheck reports whether a newer GoModel release exists.
//
// It reads a plain-text manifest on a daily schedule and on the first
// dashboard visit of each day. The request
// carries the running version, the distribution name, an anonymous install
// identifier, and the dashboard's own hostname; it never carries API keys,
// provider credentials, model names, prompts, or usage data.
//
// Every outbound request is jittered so gateways started together — a Helm
// rollout, a restarted docker-compose stack — do not query in lockstep.
package versioncheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/version"
)

// maxManifestBytes bounds what a manifest response can cost us. A version
// string is a few dozen bytes; anything larger is a misconfigured origin.
const maxManifestBytes = 256

// minRefreshInterval collapses scheduled checks that land close together, so
// a reload storm cannot turn the background timer into a request loop.
//
// It deliberately does NOT apply to dashboard visits: a browser's check is
// also how a deployment is counted, and dropping it because the timer happened
// to fire a moment earlier would silently lose that. Those are bounded by the
// per-browser daily cookie, maxConcurrentBeacons, and the daily budget.
const minRefreshInterval = time.Minute

// maxConcurrentBeacons caps how many dashboard-triggered checks may be in
// flight at once. Browsers arriving together should all be counted, but they
// must not each hold an outbound connection open.
const maxConcurrentBeacons = 8

// DefaultURL is the public release manifest served by the GoModel website.
// The channel file ("core.txt" or "pro.txt") is appended to it.
const DefaultURL = "https://gomodel.enterpilot.io/version"

// Config configures a Checker. Zero values fall back to package defaults.
type Config struct {
	Enabled   bool
	URL       string
	App       string
	Version   string
	InstallID string
	// InstallIDFunc, when set, supplies the identifier per request instead
	// of InstallID. An Identity uses it to keep retrying its database until
	// it has confirmed which id this deployment has.
	InstallIDFunc  func(context.Context) string
	Interval       time.Duration
	Timeout        time.Duration
	MaxDailyChecks int

	// Client overrides the HTTP client, for tests.
	Client *http.Client
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Status is the snapshot the /version endpoint serves. It is safe to expose:
// it contains only release metadata.
type Status struct {
	App             string `json:"app"`
	Version         string `json:"version"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	CheckedAt       string `json:"checked_at,omitempty"`
	Enabled         bool   `json:"enabled"`
}

// Checker owns the cached manifest result and the request budget.
type Checker struct {
	cfg Config
	url string
	// safeURL is url with credentials and query stripped, for errors and logs.
	safeURL string

	mu            sync.Mutex
	latest        string
	checkedAt     time.Time
	lastScheduled time.Time
	budgetDay     string
	budgetUsed    int
	inflight      bool
	beacons       int
}

// New builds a Checker for the given distribution. It never returns an error:
// a disabled or misconfigured check degrades to reporting the local version.
func New(cfg Config) *Checker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxDailyChecks <= 0 {
		cfg.MaxDailyChecks = 500
	}
	if cfg.URL == "" {
		cfg.URL = DefaultURL
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.Timeout}
	}
	manifest := manifestURL(cfg.URL, cfg.App)
	return &Checker{cfg: cfg, url: manifest, safeURL: SafeURL(manifest)}
}

func (c *Checker) installID(ctx context.Context) string {
	if c.cfg.InstallIDFunc != nil {
		return c.cfg.InstallIDFunc(ctx)
	}
	return c.cfg.InstallID
}

// manifestURL appends the distribution's channel file to the configured base.
//
// The suffix goes on the URL's path, not on the end of the string: a private
// mirror may authenticate with a query parameter, and appending after it would
// produce ".../version?token=abc/core.txt", leaving the path pointing at the
// wrong file.
func manifestURL(base, app string) string {
	channel := version.ChannelFor(app) + ".txt"
	parsed, err := url.Parse(base)
	if err != nil {
		// Left for http.NewRequest to reject, so the operator sees the parse
		// error rather than a check that silently reads the wrong path.
		return strings.TrimRight(base, "/") + "/" + channel
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + channel
	return parsed.String()
}

// LeaksQueryInCleartext reports whether a configured manifest URL would put
// its query string on the wire unencrypted. A private mirror may authenticate
// with a query token, and over plain HTTP that token is readable by anything
// on the path — redacting it from logs does not help there.
//
// Reported rather than rejected: the URL is a deliberate operator choice, an
// internal mirror on a trusted network is a legitimate setup, and refusing to
// start the gateway over a non-essential update check would be a worse
// outcome than telling the operator what they have configured.
func LeaksQueryInCleartext(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" && parsed.RawQuery != ""
}

// safeURL identifies the manifest host for errors and logs without quoting
// anything an operator may have embedded in the configured URL. A private
// mirror can carry a secret in userinfo, in the query, or in the path itself,
// so only the scheme and host survive.
func SafeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "the configured manifest host"
	}
	return parsed.Scheme + "://" + parsed.Host
}

// Enabled reports whether outbound checks are configured.
func (c *Checker) Enabled() bool { return c != nil && c.cfg.Enabled }

// Status returns the cached result without touching the network.
func (c *Checker) Status() Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	status := Status{
		App:     c.cfg.App,
		Version: c.cfg.Version,
		Latest:  c.latest,
		Enabled: c.cfg.Enabled,
	}
	if !c.checkedAt.IsZero() {
		status.CheckedAt = c.checkedAt.UTC().Format(time.RFC3339)
		status.UpdateAvailable = IsNewer(c.cfg.Version, c.latest)
	}
	return status
}

// Run performs the background schedule until ctx is cancelled. The first
// check waits a random slice of the interval (capped at ten minutes) so a
// fleet restarting together spreads its requests out.
func (c *Checker) Run(ctx context.Context) {
	if !c.Enabled() {
		return
	}
	startup := jitter(min(c.cfg.Interval, 10*time.Minute))
	timer := time.NewTimer(startup)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		// Refresh logs its own failures.
		_, _ = c.Refresh(ctx, Beacon{})
		// +/- 10% around the interval keeps a restarted fleet from
		// re-converging on the same minute after the first check.
		timer.Reset(c.cfg.Interval - c.cfg.Interval/10 + jitter(c.cfg.Interval/5))
	}
}

// Refresh fetches the manifest and updates the cache. It returns the current
// status even when the request is throttled, out of budget, or fails, so
// callers can always answer with the local version.
func (c *Checker) Refresh(ctx context.Context, beacon Beacon) (Status, error) {
	if !c.Enabled() {
		return c.Status(), nil
	}
	dashboard := beacon.dashboard()
	if !c.reserve(dashboard) {
		return c.Status(), nil
	}
	defer c.release(dashboard)

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	latest, err := c.fetch(ctx, beacon)
	if err != nil {
		// Logged here rather than at each call site: the dashboard dispatches
		// its refresh in the background and has nowhere to surface an error,
		// so a mirror that is persistently failing would otherwise be silent.
		if !errors.Is(err, context.Canceled) {
			slog.Debug("version check failed", "error", err, "host", c.safeURL, "channel", version.ChannelFor(c.cfg.App))
		}
		return c.Status(), err
	}

	now := c.cfg.Now()
	c.mu.Lock()
	c.latest = latest
	c.checkedAt = now
	c.mu.Unlock()
	return c.Status(), nil
}

// reserve claims one slot from the daily budget, which bounds every caller,
// plus the limit that applies to this trigger: scheduled checks collapse onto
// one another, dashboard visits run concurrently up to maxConcurrentBeacons.
func (c *Checker) reserve(dashboard bool) bool {
	now := c.cfg.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if dashboard {
		if c.beacons >= maxConcurrentBeacons {
			return false
		}
	} else {
		if c.inflight {
			return false
		}
		if !c.lastScheduled.IsZero() && now.Sub(c.lastScheduled) < minRefreshInterval {
			return false
		}
	}

	day := now.UTC().Format(time.DateOnly)
	if day != c.budgetDay {
		c.budgetDay = day
		c.budgetUsed = 0
	}
	if c.budgetUsed >= c.cfg.MaxDailyChecks {
		return false
	}
	c.budgetUsed++
	if dashboard {
		c.beacons++
	} else {
		// Only the scheduled cadence is throttled by this, so a busy
		// dashboard never postpones the background timer.
		c.lastScheduled = now
		c.inflight = true
	}
	return true
}

func (c *Checker) release(dashboard bool) {
	c.mu.Lock()
	if dashboard {
		c.beacons--
	} else {
		c.inflight = false
	}
	c.mu.Unlock()
}

func (c *Checker) fetch(ctx context.Context, beacon Beacon) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", fmt.Sprintf("%s/%s", strings.ReplaceAll(c.cfg.App, " ", "-"), c.cfg.Version))
	req.Header.Set("X-GoModel-Version", c.cfg.Version)
	req.Header.Set("X-GoModel-App", c.cfg.App)
	req.Header.Set("X-GoModel-Install", c.installID(ctx))
	beacon.apply(req)

	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version manifest %s returned %d", c.safeURL, resp.StatusCode)
	}
	// One byte past the cap distinguishes "at the limit" from "longer than
	// the limit", so an oversized body is rejected rather than silently
	// truncated into a plausible-looking version.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxManifestBytes {
		return "", fmt.Errorf("version manifest %s is larger than %d bytes", c.safeURL, maxManifestBytes)
	}
	latest := strings.TrimSpace(string(body))
	if latest == "" || strings.ContainsAny(latest, " \t\n<") {
		return "", fmt.Errorf("version manifest %s did not contain a version", c.safeURL)
	}
	return latest, nil
}

// jitter returns a random duration in [0, d).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}
