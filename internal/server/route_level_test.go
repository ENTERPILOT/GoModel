package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
)

// levelRouteResolver stands in for the virtual model service: "auto" is a
// route using the "auto" strategy over the rewrite test provider's model. It
// serves a request from the level the context asks for, the highest when it
// asks for none, and reports the level it chose.
type levelRouteResolver struct{}

func (levelRouteResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	selector, err := requested.Normalize()
	return selector, false, err
}

func (levelRouteResolver) ResolveModelRouteForUserPath(ctx context.Context, requested core.RequestedModelSelector) (core.ModelSelector, []string, bool, error) {
	if requested.Model != "auto" {
		selector, err := requested.Normalize()
		return selector, nil, false, err
	}
	rank := core.RouteLevelRank(core.RouteLevelFromContext(ctx))
	if rank == 0 {
		rank = core.RouteLevelRank(core.RouteLevelSmartest)
	}
	core.ReportServedRouteLevel(ctx, rank)
	return core.ModelSelector{Model: "gpt-4o-mini"}, nil, true, nil
}

// The level a rewriter names reaches the route, and the level the route
// served comes back in a response header.
func TestRouteLevelHeaderReportsTheLevelTheRouteServed(t *testing.T) {
	tests := []struct {
		name  string
		model string
		level string // what the rewriter names; "" names none
		want  string
	}{
		{name: "classified request", model: "auto", level: ext.RouteLevelLight, want: ext.RouteLevelLight},
		{name: "unclassified request gets the highest level", model: "auto", want: ext.RouteLevelSmartest},
		{name: "a model that is not an auto route sends no header", model: "gpt-4o-mini", level: ext.RouteLevelLight, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classifier := &stubRewriter{
				name: "classifier",
				rewrite: func(ext.Input) (*ext.Result, error) {
					return &ext.Result{RouteLevel: tt.level}, nil
				},
			}
			srv := New(newRewriteTestProvider(), &Config{
				RequestRewriters: []ext.RequestRewriter{classifier},
				ModelResolver:    levelRouteResolver{},
			})

			rec := postJSON(t, srv, "/v1/chat/completions",
				`{"model":"`+tt.model+`","messages":[{"role":"user","content":"hi"}]}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.want, rec.Header().Get(routeLevelHeader))
		})
	}
}
