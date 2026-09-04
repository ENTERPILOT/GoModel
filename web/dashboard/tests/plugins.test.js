// GET /admin/plugins row normalization and the phase helpers shared by the
// guardrail list, workflow editor, and chart.

import test from "node:test";
import assert from "node:assert/strict";

import {
  normalizePlugins,
  pluginHealthy,
  pluginRouteFields,
  pluginSourceIsBuiltin,
} from "../src/lib/utils/plugins.js";
import {
  isWorkflowPhase,
  normalizePhases,
  normalizeWorkflowPhase,
  phaseLabel,
  phasesSupport,
} from "../src/lib/utils/pluginPhases.js";

test("normalizePlugins fills every field and drops nameless rows", () => {
  const plugins = normalizePlugins([
    {
      name: "latency-aware",
      version: "1.0.0",
      kinds: ["route"],
      source: "/etc/gomodel/plugins/latency.so",
      route_fields: [{ key: "p95_window", input: "text", scope: "route" }],
    },
    { name: "string_replace", source: "builtin", health: "error", error: "init failed" },
    { version: "0.0.1" },
    null,
  ]);

  assert.deepEqual(
    plugins.map((plugin) => plugin.name),
    ["latency-aware", "string_replace"],
  );
  assert.deepEqual(plugins[0].route_fields.map((field) => field.key), ["p95_window"]);
  assert.equal(plugins[0].health, "ok");
  assert.equal(pluginHealthy(plugins[0]), true);
  assert.equal(pluginSourceIsBuiltin(plugins[0]), false);
  assert.deepEqual(plugins[1].kinds, []);
  assert.equal(pluginHealthy(plugins[1]), false);
  assert.equal(pluginSourceIsBuiltin(plugins[1]), true);
});

test("normalizePlugins derives route_fields from scoped fields when absent", () => {
  const [plugin] = normalizePlugins([
    {
      name: "acme",
      fields: [
        { key: "api_key", input: "secret" },
        { key: "budget_ms", input: "number", scope: "route" },
      ],
    },
  ]);
  assert.deepEqual(plugin.route_fields.map((field) => field.key), ["budget_ms"]);
  assert.deepEqual(pluginRouteFields([plugin], "acme").map((field) => field.key), [
    "budget_ms",
  ]);
  assert.deepEqual(pluginRouteFields([plugin], "missing"), []);
  assert.deepEqual(pluginRouteFields(undefined, "acme"), []);
});

test("phase helpers default to prompt-only and label known phases", () => {
  assert.deepEqual(normalizePhases(undefined), ["prompt"]);
  assert.deepEqual(normalizePhases([]), ["prompt"]);
  assert.deepEqual(normalizePhases([" Prompt", "response", "response"]), [
    "prompt",
    "response",
  ]);
  assert.equal(phasesSupport(undefined, "prompt"), true);
  assert.equal(phasesSupport(undefined, "response"), false);
  assert.equal(phasesSupport(["prompt", "stream"], "stream"), true);
  assert.equal(normalizeWorkflowPhase("STREAM"), "stream");
  assert.equal(normalizeWorkflowPhase("route"), "prompt");
  assert.equal(isWorkflowPhase("route"), false);
  assert.equal(isWorkflowPhase("response"), true);
  assert.equal(phaseLabel("prompt"), "Prompt");
  assert.equal(phaseLabel("stream"), "Stream");
  assert.equal(phaseLabel("custom"), "custom");
});
