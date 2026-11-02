// Every free-text filter in the dashboard follows one rule: the substring
// level decides, and the fuzzy level only runs when the substring level found
// nothing. These tests drive the real filter of each page, so the wiring is
// covered and not just the shared matcher.
//
// Each fixture holds two rows: "dep1017" (matched by the query as a substring)
// and "deepseek-v4.1-flash:1017" (only reachable by subsequence).

import test from "node:test";
import assert from "node:assert/strict";

import { filterAuthKeys } from "../src/pages/auth-keys/authKeysLogic.js";
import { filterAndSortBudgets } from "../src/pages/budgets/budgets-helpers.js";
import { filterGuardrails } from "../src/pages/guardrails/guardrails-logic.js";
import { filterDisplayModels } from "../src/pages/models/displayRows.js";
import { filterMcpServers, mcpToolPickerRows } from "../src/pages/mcp-servers/mcp-servers.js";
import { filterProviderCredentials } from "../src/pages/providers-config/providersConfigLogic.js";
import { filteredRateLimits } from "../src/pages/rate-limits/rateLimitsLogic.js";
import { visibleUserPathRows } from "../src/pages/usage/user-path-tree.js";
import { filterUserNodes } from "../src/pages/users/usersLogic.js";
import { filterWorkflows } from "../src/pages/workflows/workflowsLogic.js";

const FUZZY_ID = "deepseek-v4.1-flash:1017";
const SUBSTRING_ID = "dep1017";
const QUERY = "dep1017";

test("the API Keys filter reaches a key by subsequence", () => {
  const keys = [
    { name: FUZZY_ID, user_path: "/team" },
    { name: SUBSTRING_ID, user_path: "/team" },
  ];
  // "dep1017" is a substring of one name, so the fuzzy-only key stays out.
  assert.deepEqual(
    filterAuthKeys(keys, { query: QUERY }).map((key) => key.name),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    filterAuthKeys(keys, { query: "dk1017" }).map((key) => key.name),
    [FUZZY_ID],
  );
});

test("the MCP servers filter reaches a server by subsequence", () => {
  const servers = [{ name: FUZZY_ID }, { name: SUBSTRING_ID }];
  assert.deepEqual(
    filterMcpServers(servers, QUERY).map((server) => server.name),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    filterMcpServers(servers, "dk1017").map((server) => server.name),
    [FUZZY_ID],
  );
});

test("the MCP tool picker reaches a tool by subsequence", () => {
  const discovered = [{ name: FUZZY_ID, description: "" }, { name: SUBSTRING_ID, description: "" }];
  const form = { tool_names: [] };
  assert.deepEqual(
    mcpToolPickerRows(form, discovered, QUERY).map((row) => row.name),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    mcpToolPickerRows(form, discovered, "dk1017").map((row) => row.name),
    [FUZZY_ID],
  );
});

test("the rate limits filter reaches a rule by subsequence", () => {
  const rules = [
    { scope: "user_path", subject: FUZZY_ID, user_path: FUZZY_ID, period_seconds: 86400, period_label: "day" },
    { scope: "user_path", subject: SUBSTRING_ID, user_path: SUBSTRING_ID, period_seconds: 86400, period_label: "day" },
  ];
  assert.deepEqual(
    filteredRateLimits(rules, QUERY).map((rule) => rule.subject),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    filteredRateLimits(rules, "dk1017").map((rule) => rule.subject),
    [FUZZY_ID],
  );
});

test("the providers filter reaches a row by subsequence", () => {
  const rows = [{ name: FUZZY_ID, type: "openai" }, { name: SUBSTRING_ID, type: "openai" }];
  assert.deepEqual(
    filterProviderCredentials(rows, QUERY).map((row) => row.name),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    filterProviderCredentials(rows, "dk1017").map((row) => row.name),
    [FUZZY_ID],
  );
});

test("the workflows filter reaches a workflow by subsequence", () => {
  const workflows = [{ name: FUZZY_ID }, { name: SUBSTRING_ID }];
  assert.deepEqual(
    filterWorkflows(workflows, QUERY).map((workflow) => workflow.name),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    filterWorkflows(workflows, "dk1017").map((workflow) => workflow.name),
    [FUZZY_ID],
  );
});

test("the guardrails filter reaches a guardrail by subsequence", () => {
  const guardrails = [{ name: FUZZY_ID, type: "llmjudge" }, { name: SUBSTRING_ID, type: "llmjudge" }];
  assert.deepEqual(
    filterGuardrails(guardrails, QUERY).map((guardrail) => guardrail.name),
    [SUBSTRING_ID],
  );
  assert.deepEqual(
    filterGuardrails(guardrails, "dk1017").map((guardrail) => guardrail.name),
    [FUZZY_ID],
  );
});

test("the budgets filter reaches a budget by subsequence", () => {
  const budgets = [
    { user_path: "/team/" + FUZZY_ID, period_seconds: 86400, period_label: "daily" },
    { user_path: "/team/" + SUBSTRING_ID, period_seconds: 86400, period_label: "daily" },
  ];
  assert.deepEqual(
    filterAndSortBudgets(budgets, QUERY, "user_path").map((budget) => budget.user_path),
    ["/team/" + SUBSTRING_ID],
  );
  assert.deepEqual(
    filterAndSortBudgets(budgets, "dk1017", "user_path").map((budget) => budget.user_path),
    ["/team/" + FUZZY_ID],
  );
});

test("the users filter reaches a node by subsequence", () => {
  const nodes = [
    { user_path: "/team/" + FUZZY_ID, description: "", allowed_models: [] },
    { user_path: "/team/" + SUBSTRING_ID, description: "", allowed_models: [] },
  ];
  assert.deepEqual(
    filterUserNodes(nodes, QUERY).map((node) => node.user_path),
    ["/team/" + SUBSTRING_ID],
  );
  assert.deepEqual(
    filterUserNodes(nodes, "dk1017").map((node) => node.user_path),
    ["/team/" + FUZZY_ID],
  );
});

test("the user path tree reaches a node by subsequence and keeps its ancestors", () => {
  const tree = [
    {
      path: "/team",
      name: "team",
      children: [
        { path: "/team/" + FUZZY_ID, name: FUZZY_ID, children: [] },
        { path: "/team/" + SUBSTRING_ID, name: SUBSTRING_ID, children: [] },
      ],
    },
  ];
  assert.deepEqual(
    visibleUserPathRows(tree, new Set(), QUERY).map((row) => row.path),
    ["/team", "/team/" + SUBSTRING_ID],
  );
  assert.deepEqual(
    visibleUserPathRows(tree, new Set(), "dk1017").map((row) => row.path),
    ["/team", "/team/" + FUZZY_ID],
  );
});

test("the Models page filter reaches a row by subsequence", () => {
  const rows = [
    { display_name: "vllm-qa/" + FUZZY_ID },
    { display_name: "vllm-qa/" + SUBSTRING_ID },
  ];
  assert.deepEqual(
    filterDisplayModels(rows, QUERY).map((row) => row.display_name),
    ["vllm-qa/" + SUBSTRING_ID],
  );
  assert.deepEqual(
    filterDisplayModels(rows, "dk1017").map((row) => row.display_name),
    ["vllm-qa/" + FUZZY_ID],
  );
});
