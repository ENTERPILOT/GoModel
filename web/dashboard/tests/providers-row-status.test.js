// Live status shown on the Providers page rows.
import test from "node:test";
import assert from "node:assert/strict";

import {
  providerRowNoStatusTitle,
  providerRowStatus,
  providerStatusByName,
} from "../src/pages/providers-config/providerRowStatus.js";

const statusProviders = [
  {
    name: "openai",
    status: "healthy",
    status_label: "Healthy",
    runtime: { discovered_model_count: 42 },
  },
  {
    name: "anthropic",
    status: "unhealthy",
    status_label: "Unhealthy",
    last_error: "401 invalid api key",
    runtime: { discovered_model_count: 0 },
  },
  {
    name: "stale",
    status: "unhealthy",
    status_label: "Offline",
    runtime: { discovered_model_count: 7, inventory_stale: true },
  },
  { name: "  ", status: "healthy" },
  null,
];

test("providerStatusByName indexes status rows by trimmed name", () => {
  const byName = providerStatusByName(statusProviders);
  assert.deepEqual([...byName.keys()], ["openai", "anthropic", "stale"]);
  assert.equal(providerStatusByName(undefined).size, 0);
});

test("providerRowStatus pairs a row with its live status", () => {
  const byName = providerStatusByName(statusProviders);
  const cases = [
    {
      name: "healthy dashboard row",
      row: { name: "openai", enabled: true },
      want: { name: "openai", modelCount: 42, lastError: "" },
    },
    {
      name: "config-declared row is matched like any other",
      row: { name: "anthropic", enabled: true, managed: true },
      want: { name: "anthropic", modelCount: 0, lastError: "401 invalid api key" },
    },
    {
      name: "stale inventory counts as no models",
      row: { name: "stale", enabled: true },
      want: { name: "stale", modelCount: 0, lastError: "" },
    },
    {
      name: "row name is trimmed before matching",
      row: { name: " openai ", enabled: true },
      want: { name: "openai", modelCount: 42, lastError: "" },
    },
    { name: "disabled row has no live status", row: { name: "openai", enabled: false }, want: null },
    { name: "unknown provider", row: { name: "groq", enabled: true }, want: null },
    { name: "missing row", row: null, want: null },
  ];
  for (const { name, row, want } of cases) {
    const got = providerRowStatus(row, byName);
    if (want === null) {
      assert.equal(got, null, name);
      continue;
    }
    assert.equal(got.provider.name, want.name, name);
    assert.equal(got.modelCount, want.modelCount, name);
    assert.equal(got.lastError, want.lastError, name);
  }
});

test("providerRowStatus without a status index shows nothing", () => {
  assert.equal(providerRowStatus({ name: "openai", enabled: true }, undefined), null);
});

test("providerRowNoStatusTitle blames disabling only for a disabled row", () => {
  assert.match(providerRowNoStatusTitle({ name: "openai", enabled: false }), /disabled/);
  assert.doesNotMatch(providerRowNoStatusTitle({ name: "openai", enabled: true }), /disabled/);
  assert.doesNotMatch(providerRowNoStatusTitle(null), /disabled/);
});
