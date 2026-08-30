package ext

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namedRewriter struct{ name string }

type namedAuthenticator struct{ name string }

func (a *namedAuthenticator) Name() string { return a.name }

func (a *namedAuthenticator) AuthenticateRequest(context.Context, *http.Request) (*Authentication, error) {
	return nil, nil
}

type testRuntimeSetting struct{ value string }

func (s *testRuntimeSetting) Descriptor() SettingDescriptor {
	return SettingDescriptor{Key: "test.setting", Value: s.value}
}

func (s *testRuntimeSetting) Apply(value string) error {
	s.value = value
	return nil
}

func (r *namedRewriter) Name() string { return r.name }

func (r *namedRewriter) Rewrite(_ context.Context, _ Input) (*Result, error) {
	return nil, nil
}

func TestRegistryPreservesRegistrationOrder(t *testing.T) {
	reg := &Registry{}
	names := []string{"first", "second", "third"}
	for _, name := range names {
		reg.RegisterRewriter(&namedRewriter{name: name})
	}

	got := reg.Rewriters()
	require.Len(t, got, len(names))
	for i, rw := range got {
		assert.Equal(t, names[i], rw.Name())
	}
}

func TestRegistrySnapshotsAreIsolated(t *testing.T) {
	reg := &Registry{}
	reg.RegisterRewriter(&namedRewriter{name: "one"})
	reg.AddPublicPaths("/sso/callback")

	rewriters := reg.Rewriters()
	paths := reg.PublicPaths()

	reg.RegisterRewriter(&namedRewriter{name: "two"})
	reg.AddPublicPaths("/sso/login")

	assert.Len(t, rewriters, 1, "earlier snapshot must not grow")
	assert.Equal(t, []string{"/sso/callback"}, paths)
	assert.Len(t, reg.Rewriters(), 2)
	assert.Len(t, reg.PublicPaths(), 2)
}

func TestRegistryCollectsMiddlewareAndRoutes(t *testing.T) {
	reg := &Registry{}
	reg.UseOuterMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
	reg.UseMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
	reg.RegisterRoutes(func(_ *echo.Echo) {})

	assert.Len(t, reg.OuterMiddleware(), 1)
	assert.Len(t, reg.Middleware(), 1)
	assert.Len(t, reg.Routes(), 1)
}

func TestRegistryCollectsRequestAuthenticators(t *testing.T) {
	reg := &Registry{}
	reg.RegisterAuthenticator(&namedAuthenticator{name: "oidc"})

	snapshot := reg.Authenticators()
	require.Len(t, snapshot, 1)
	assert.Equal(t, "oidc", snapshot[0].Name())
	reg.RegisterAuthenticator(&namedAuthenticator{name: "other"})
	assert.Len(t, snapshot, 1, "earlier snapshot must not grow")
}

func TestRegistryCollectsRuntimeSettings(t *testing.T) {
	reg := &Registry{}
	reg.RegisterSetting(&testRuntimeSetting{value: "high"})

	snapshot := reg.Settings()
	require.Len(t, snapshot, 1)
	reg.RegisterSetting(&testRuntimeSetting{value: "low"})
	assert.Len(t, snapshot, 1, "earlier snapshot must not grow")
	assert.Len(t, reg.Settings(), 2)
}

func TestRegistryCapabilitiesDefaultOffAndCanBeEnabled(t *testing.T) {
	reg := &Registry{}

	assert.False(t, reg.HasCapability(CapabilityQuotaTemplates))
	reg.EnableCapability("")
	assert.False(t, reg.HasCapability(CapabilityQuotaTemplates))

	reg.EnableCapability(CapabilityQuotaTemplates)
	assert.True(t, reg.HasCapability(CapabilityQuotaTemplates))
}

func TestRegistryConcurrentRegistration(t *testing.T) {
	reg := &Registry{}
	const workers = 16

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			reg.RegisterRewriter(&namedRewriter{name: "w"})
			reg.UseOuterMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
			reg.UseMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
			reg.AddPublicPaths("/p")
			reg.EnableCapability(CapabilityQuotaTemplates)
			_ = reg.Rewriters()
			_ = reg.HasCapability(CapabilityQuotaTemplates)
		})
	}
	wg.Wait()

	assert.Len(t, reg.Rewriters(), workers)
	assert.Len(t, reg.OuterMiddleware(), workers)
	assert.Len(t, reg.Middleware(), workers)
	assert.Len(t, reg.PublicPaths(), workers)
	assert.True(t, reg.HasCapability(CapabilityQuotaTemplates))
}

type namedSelector struct{ name string }

func (s *namedSelector) Name() string                       { return s.name }
func (s *namedSelector) Select(RouteRequest) (string, bool) { return "", false }
func (s *namedSelector) OnAttemptStart(RouteTarget)         {}
func (s *namedSelector) OnAttemptEnd(RouteOutcome)          {}

func TestRegistryRouteSelectorSingleSlot(t *testing.T) {
	tests := []struct {
		name     string
		register []RouteSelector
		want     string // Name() of the expected selector; "" means nil
	}{
		{name: "unset", register: nil, want: ""},
		{name: "single registration", register: []RouteSelector{&namedSelector{name: "only"}}, want: "only"},
		{name: "later registration replaces earlier", register: []RouteSelector{&namedSelector{name: "first"}, &namedSelector{name: "second"}}, want: "second"},
		{name: "nil registration resets the slot", register: []RouteSelector{&namedSelector{name: "first"}, nil}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &Registry{}
			for _, sel := range tt.register {
				reg.RegisterRouteSelector(sel)
			}
			got := reg.RouteSelector()
			if tt.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.Name())
		})
	}
}

func TestRouteTargetQualified(t *testing.T) {
	tests := []struct {
		name   string
		target RouteTarget
		want   string
	}{
		{name: "provider and model", target: RouteTarget{Provider: "openai", Model: "gpt-4o"}, want: "openai/gpt-4o"},
		{name: "empty provider keeps the separator", target: RouteTarget{Model: "gpt-4o"}, want: "/gpt-4o"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.target.Qualified())
		})
	}
}

func TestRejectionErrorMessage(t *testing.T) {
	err := &RejectionError{Status: 422, Code: "policy_violation", Message: "blocked by policy"}
	assert.Equal(t, "request rejected (422 policy_violation): blocked by policy", err.Error())
}
