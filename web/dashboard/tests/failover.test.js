// Pure-logic cases ported from the legacy
// internal/admin/dashboard/static/js/modules/failover.test.cjs. Network/store
// wiring tests are skipped; the payload, filtering, and selection logic they
// exercised is covered here against failover-logic.js.

import test from "node:test";
import assert from "node:assert/strict";

import {
  qualifiedModelName,
  failoverPrimaryModel,
  failoverTargets,
  normalizeFailoverRules,
  failoverTargetLabel,
  failoverRuleStatus,
  hasActiveFailoverMapping,
  failoverButtonClass,
  failoverButtonLabel,
  failoverFormTargets,
  splitFailoverTargets,
  failoverRulePayload,
  buildDraftSelections,
  failoverDraftSelected,
  selectedFailoverDrafts,
  allFailoverDraftsSelected,
  filterFailoverDrafts,
  failoverDraftPayload,
} from "../src/pages/models/failover-logic.js";

test("failover rule status uses localized messages", () => {
  assert.equal(failoverRuleStatus({ enabled: true }), "On");
  assert.equal(failoverRuleStatus({ enabled: false }), "Off");
  assert.equal(failoverRuleStatus({ enabled: true, managed: true }), "Config");
});

test("failover action button marks rows with enabled fallback mappings active", () => {
  const row = {
    selector: "openai/gpt-4o",
    display_name: "gpt-4o",
    model: { id: "gpt-4o" },
    provider_name: "openai",
  };
  const rules = [
    {
      primary_model: "openai/gpt-4o",
      fallback_models: ["anthropic/claude-3-5-sonnet"],
      enabled: true,
    },
    {
      primary_model: "openai/gpt-4o-mini",
      fallback_models: ["anthropic/claude-3-haiku"],
      enabled: false,
    },
    { primary_model: "openai/gpt-4.1", fallback_models: [], enabled: true },
  ];

  assert.equal(hasActiveFailoverMapping(rules, row), true);
  assert.equal(failoverButtonClass(rules, row), "table-action-btn-failover-active");
  assert.equal(failoverButtonLabel(rules, row), "Edit failover for gpt-4o (active)");
  assert.equal(
    hasActiveFailoverMapping(rules, { ...row, selector: "openai/gpt-4o-mini" }),
    false,
  );
  assert.equal(
    hasActiveFailoverMapping(rules, { ...row, selector: "openai/gpt-4.1" }),
    false,
  );
  assert.equal(hasActiveFailoverMapping(rules, { ...row, is_alias: true }), false);
});

test("qualifiedModelName prefers selector, then provider name, then type", () => {
  assert.equal(qualifiedModelName({ selector: "openai/gpt-4o" }), "openai/gpt-4o");
  assert.equal(
    qualifiedModelName({ model: { id: "gpt-4o" }, provider_name: "azure-eu" }),
    "azure-eu/gpt-4o",
  );
  assert.equal(
    qualifiedModelName({ model: { id: "gpt-4o" }, provider_type: "openai" }),
    "openai/gpt-4o",
  );
  assert.equal(
    qualifiedModelName({ model: { id: "meta/llama-3" }, provider_type: "openrouter" }),
    "meta/llama-3",
  );
  assert.equal(qualifiedModelName(null), "");
  assert.equal(qualifiedModelName({ model: {} }), "");
});

test("normalizeFailoverRules mirrors primary/fallbacks into source/targets", () => {
  assert.deepEqual(normalizeFailoverRules(null), []);
  assert.deepEqual(normalizeFailoverRules("nope"), []);
  const normalized = normalizeFailoverRules([
    {
      primary_model: " openai/gpt-4o ",
      fallback_models: ["anthropic/claude-3-5-sonnet"],
      enabled: true,
    },
    { source: "openai/gpt-4.1", targets: ["gemini/gemini-2.5-pro"] },
  ]);
  assert.equal(normalized[0].source, "openai/gpt-4o");
  assert.deepEqual(normalized[0].targets, ["anthropic/claude-3-5-sonnet"]);
  assert.equal(normalized[1].source, "openai/gpt-4.1");
  assert.deepEqual(normalized[1].targets, ["gemini/gemini-2.5-pro"]);
  assert.equal(failoverPrimaryModel(normalized[1]), "openai/gpt-4.1");
  assert.deepEqual(failoverTargets(normalized[1]), ["gemini/gemini-2.5-pro"]);
  assert.equal(failoverTargetLabel(normalized[1]), "gemini/gemini-2.5-pro");
  assert.equal(failoverTargetLabel({}), "-");
});

test("failover rule payload flattens and trims editor form targets", () => {
  const form = {
    source: " openai/gpt-4o ",
    target_model: " anthropic/claude-3-5-sonnet ",
    targets: [{ model: "" }, { model: " gemini/gemini-2.5-pro " }, null],
    enabled: true,
  };
  assert.deepEqual(failoverFormTargets(form), [
    "anthropic/claude-3-5-sonnet",
    "gemini/gemini-2.5-pro",
  ]);
  assert.deepEqual(failoverRulePayload(form), {
    primary_model: "openai/gpt-4o",
    fallback_models: ["anthropic/claude-3-5-sonnet", "gemini/gemini-2.5-pro"],
    enabled: true,
  });
  assert.equal(failoverRulePayload({ ...form, enabled: false }).enabled, false);
  assert.equal(failoverRulePayload({ ...form, enabled: undefined }).enabled, true);
});

test("splitFailoverTargets splits generated targets into form shape", () => {
  assert.deepEqual(
    splitFailoverTargets([
      "anthropic/claude-3-5-sonnet",
      " gemini/gemini-2.5-pro ",
      "",
    ]),
    {
      target_model: "anthropic/claude-3-5-sonnet",
      targets: [{ model: "gemini/gemini-2.5-pro" }],
    },
  );
  assert.deepEqual(splitFailoverTargets(null), { target_model: "", targets: [] });
  // Round trip: form shape back to a flat list.
  const split = splitFailoverTargets(["a/x", "b/y", "c/z"]);
  assert.deepEqual(failoverFormTargets(split), ["a/x", "b/y", "c/z"]);
});

test("failoverDraftPayload trims and drops empty fallback entries", () => {
  assert.deepEqual(
    failoverDraftPayload({
      primary_model: "openai/gpt-4o",
      fallback_models: [" anthropic/claude-3-5-sonnet ", "", null],
      enabled: true,
    }),
    {
      primary_model: "openai/gpt-4o",
      fallback_models: ["anthropic/claude-3-5-sonnet"],
      enabled: true,
    },
  );
  assert.equal(
    failoverDraftPayload({ primary_model: "x", fallback_models: [], enabled: false })
      .enabled,
    false,
  );
});

test("draft selection helpers preselect, count, and save selected drafts only", () => {
  const drafts = [
    {
      primary_model: "openai/gpt-4o",
      fallback_models: ["anthropic/claude-3-5-sonnet"],
      enabled: true,
    },
    {
      primary_model: "openai/gpt-4.1",
      fallback_models: ["gemini/gemini-2.5-pro"],
      enabled: true,
    },
  ];
  let selections = buildDraftSelections(drafts);
  assert.equal(failoverDraftSelected(selections, drafts[0]), true);
  assert.equal(failoverDraftSelected(selections, drafts[1]), true);
  assert.equal(allFailoverDraftsSelected(drafts, selections), true);

  // Deselect the second draft (legacy setFailoverDraftSelected(rule, false)).
  selections = { ...selections, "openai/gpt-4.1": false };
  const selected = selectedFailoverDrafts(drafts, selections);
  assert.equal(selected.length, 1);
  assert.deepEqual(failoverDraftPayload(selected[0]), {
    primary_model: "openai/gpt-4o",
    fallback_models: ["anthropic/claude-3-5-sonnet"],
    enabled: true,
  });
  assert.equal(allFailoverDraftsSelected(drafts, selections), false);
  assert.equal(allFailoverDraftsSelected([], {}), false);
});

test("failover draft filter counter and bulk toggle reflect generated drafts", () => {
  const drafts = [
    {
      primary_model: "openai/gpt-4o",
      fallback_models: ["anthropic/claude-3-5-sonnet"],
      enabled: true,
    },
    {
      primary_model: "openai/gpt-4.1",
      fallback_models: ["gemini/gemini-2.5-pro"],
      enabled: true,
    },
    {
      primary_model: "groq/llama-3.3-70b",
      fallback_models: ["openrouter/meta-llama/llama-3.3-70b-instruct"],
      enabled: true,
    },
  ];
  let selections = buildDraftSelections(drafts);
  assert.equal(selectedFailoverDrafts(drafts, selections).length, 3);
  assert.equal(allFailoverDraftsSelected(drafts, selections), true);

  // Filter matches primary model or any fallback target, case-insensitive.
  const filtered = filterFailoverDrafts(drafts, "gemini");
  assert.equal(filtered.length, 1);
  assert.equal(failoverPrimaryModel(filtered[0]), "openai/gpt-4.1");
  assert.equal(filterFailoverDrafts(drafts, "  GROQ ").length, 1);
  assert.equal(filterFailoverDrafts(drafts, "").length, 3);
  assert.equal(filterFailoverDrafts(drafts, "no-match").length, 0);

  // Toggle all off (legacy toggleAllFailoverDrafts when everything selected),
  // then back on; the filter never affects the selection set.
  selections = {};
  assert.equal(selectedFailoverDrafts(drafts, selections).length, 0);
  assert.equal(filterFailoverDrafts(drafts, "gemini").length, 1);
  selections = buildDraftSelections(drafts);
  assert.equal(selectedFailoverDrafts(drafts, selections).length, 3);
  assert.equal(allFailoverDraftsSelected(drafts, selections), true);
});
