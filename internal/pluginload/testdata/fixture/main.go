// Package main is the test fixture plugin for internal/pluginload. It is
// built with -buildmode=plugin by the loader tests and is not part of the
// gateway build (testdata directories are skipped by ./... patterns).
package main

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/enterpilot/gomodel/pluginapi"
)

// GoModelBuildInfo mirrors what `gomodel plugin build` stamps into a plugin.
var GoModelBuildInfo = pluginapi.BuildInfo{
	GoVersion:        "go-fixture",
	PluginAPIVersion: pluginapi.Version,
}

var instances atomic.Int32

// GoModelPlugin is the constructor symbol the loader looks up. Each call
// returns a fresh instance, which the tests verify.
func GoModelPlugin() pluginapi.Plugin {
	return &fixture{serial: int(instances.Add(1))}
}

type fixture struct {
	serial int
}

func (f *fixture) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        "fixture",
		Version:     "1.2.3",
		Description: "loader test fixture",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt},
		ConfigSchema: []pluginapi.Field{
			{Key: "greeting", Label: "Greeting", Default: "hello"},
		},
	}
}

func (f *fixture) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (f *fixture) Close(context.Context) error                                 { return nil }

// Serial identifies the instance so tests can tell instances apart.
func (f *fixture) Serial() int { return f.serial }

func (f *fixture) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt != nil && x.Prompt.LastUser() != nil && x.Prompt.LastUser().Text() == "block me" {
		return pluginapi.Block(0, "fixture", "blocked by fixture"), nil
	}
	return pluginapi.Allow(), nil
}
