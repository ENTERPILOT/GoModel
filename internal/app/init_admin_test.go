package app

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/require"
)

func TestDashboardVirtualModelStrategies_IncludesRoutePlugins(t *testing.T) {
	cases := []struct {
		name            string
		adaptiveRouting bool
		plugins         []string
		want            string
	}{
		{name: "no plugins", want: "round_robin,cost,failover"},
		{name: "plugins appended", plugins: []string{"cheapest_healthy", " latency_aware "}, want: "round_robin,cost,failover,plugin:cheapest_healthy,plugin:latency_aware"},
		{name: "adaptive before plugins", adaptiveRouting: true, plugins: []string{"cheapest_healthy"}, want: "round_robin,cost,failover,adaptive,plugin:cheapest_healthy"},
		{name: "blank names skipped", plugins: []string{"", "x"}, want: "round_robin,cost,failover,plugin:x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dashboardVirtualModelStrategies(tc.adaptiveRouting, tc.plugins)
			require.Equal(t, tc.want, got)

		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRouteOutcome_MapsClientResults(t *testing.T) {
	var _ net.Error = timeoutErr{}
	cases := []struct {
		name        string
		info        llmclient.ResponseInfo
		wantSuccess bool
		wantTimeout bool
	}{
		{name: "200", info: llmclient.ResponseInfo{StatusCode: 200, Duration: time.Second}, wantSuccess: true},
		{name: "500 with error", info: llmclient.ResponseInfo{StatusCode: 500, Error: errors.New("upstream")}},
		{name: "4xx without error", info: llmclient.ResponseInfo{StatusCode: 429}},
		{name: "deadline", info: llmclient.ResponseInfo{Error: context.DeadlineExceeded}, wantTimeout: true},
		{name: "net timeout", info: llmclient.ResponseInfo{Error: timeoutErr{}}, wantTimeout: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.info.Provider, tc.info.Model = "openai", "gpt-4o"
			got := routeOutcome("smart", tc.info)
			require.Equal(t, "smart", got.Source)
			require.Equal(t, "openai/gpt-4o", got.Target.Qualified(), "outcome = %+v, want source smart and target openai/gpt-4o", got)
			require.Equal(t, tc.wantSuccess, got.Success)
			require.Equal(t, tc.wantTimeout, got.Timeout, "outcome = %+v, want success=%v timeout=%v", got, tc.wantSuccess, tc.wantTimeout)
			require.Equal(t, tc.info.StatusCode, got.StatusCode)
			require.Equal(t, tc.info.Duration, got.Latency, "outcome = %+v, want status and latency copied", got)

		})
	}
}
