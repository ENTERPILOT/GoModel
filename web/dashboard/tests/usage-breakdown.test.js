// Usage page: user path tree picker, URL state and breakdown (pivot) helpers.

import test from "node:test";
import assert from "node:assert/strict";

import {
  buildUserPathTree,
  customUserPath,
  normalizeUserPath,
  toggleUserPath,
  userPathCovered,
  visibleUserPathRows,
} from "../src/pages/usage/user-path-tree.js";
import { defaultPivotConfig, usageViewFromQuery, usageViewToQuery } from "../src/pages/usage/usage-url-state.js";
import {
  formatPivotValue,
  pivotChartConfig,
  pivotCsv,
  pivotKeyLabel,
  pivotTable,
} from "../src/pages/usage/pivot-helpers.js";

const PATHS = ["/team/alice/laptop", "/team/alice/ci", "/team/bob", "/ops", "/"];

test("normalizeUserPath mirrors the gateway canonical form", () => {
  assert.equal(normalizeUserPath("team//alice/ "), "/team/alice");
  assert.equal(normalizeUserPath("/"), "/");
  assert.equal(normalizeUserPath("  "), "");
});

test("buildUserPathTree groups paths by common prefix", () => {
  const tree = buildUserPathTree(PATHS);
  assert.deepEqual(
    tree.map((node) => node.path),
    ["/ops", "/team"],
  );
  const team = tree[1];
  assert.deepEqual(
    team.children.map((node) => node.path),
    ["/team/alice", "/team/bob"],
  );
  assert.deepEqual(
    team.children[0].children.map((node) => node.name),
    ["ci", "laptop"],
  );
});

test("visibleUserPathRows shows expanded nodes, or every match with its ancestors", () => {
  const tree = buildUserPathTree(PATHS);
  assert.deepEqual(
    visibleUserPathRows(tree, new Set(), "").map((row) => row.path),
    ["/ops", "/team"],
  );
  const opened = visibleUserPathRows(tree, new Set(["/team"]), "");
  assert.deepEqual(
    opened.map((row) => [row.path, row.depth, row.hasChildren, row.expanded]),
    [
      ["/ops", 0, false, false],
      ["/team", 0, true, true],
      ["/team/alice", 1, true, false],
      ["/team/bob", 1, false, false],
    ],
  );
  assert.deepEqual(
    visibleUserPathRows(tree, new Set(), "LAPTOP").map((row) => row.path),
    ["/team", "/team/alice", "/team/alice/laptop"],
  );
});

test("userPathCovered marks paths inside a selected subtree", () => {
  assert.equal(userPathCovered("/team/alice", ["/team"]), true);
  assert.equal(userPathCovered("/team", ["/team"]), false);
  assert.equal(userPathCovered("/teams", ["/team"]), false);
  assert.equal(userPathCovered("/ops", ["/"]), true);
});

test("customUserPath offers only unknown, unselected paths", () => {
  const tree = buildUserPathTree(PATHS);
  assert.equal(customUserPath("team/carol", tree, []), "/team/carol");
  assert.equal(customUserPath("/team/alice", tree, []), "");
  assert.equal(customUserPath("/team/carol", tree, ["/team/carol"]), "");
  assert.equal(customUserPath("", tree, []), "");
  assert.deepEqual(toggleUserPath(["/a"], "/b"), ["/a", "/b"]);
  assert.deepEqual(toggleUserPath(["/a", "/b"], "/a"), ["/b"]);
});

test("usage view round-trips through the URL query", () => {
  const view = {
    filters: {
      models: ["gpt-5", "claude"],
      providers: [],
      labels: ["env:dev,local"],
      userPaths: ["/team/alice"],
      authKeys: ["key-a"],
      endpoints: ["/v1/messages"],
    },
    session: "s-1",
    window: { start: "2026-01-01", end: "2026-03-31" },
    pivot: { rows: "model", columns: "", metric: "tokens", interval: "weekly", depth: 2 },
  };
  const qs = usageViewToQuery(view);
  assert.match(qs, /model=gpt-5&model=claude/);
  assert.match(qs, /columns=none/);
  assert.deepEqual(usageViewFromQuery("?" + qs), view);
});

test("usage view query leaves defaults out and falls back on bad values", () => {
  assert.equal(usageViewToQuery({ window: { days: 30 }, pivot: defaultPivotConfig() }), "days=30");
  const parsed = usageViewFromQuery("rows=bogus&columns=user_path&metric=latency&days=9999&user_path_depth=-3");
  assert.deepEqual(parsed.pivot, { rows: "user_path", columns: "", metric: "cost", interval: "monthly", depth: 0 });
  assert.deepEqual(parsed.window, { days: 365 });
  assert.equal(usageViewFromQuery("").pivot, null);
  assert.equal(usageViewFromQuery("start_date=2026-02-01&end_date=bad").window, null);
  assert.deepEqual(usageViewFromQuery("start_date=2026-03-01&end_date=2026-01-01").window, {
    start: "2026-01-01",
    end: "2026-03-01",
  });
});

const BREAKDOWN = {
  row_dimension: "user_path",
  column_dimension: "period",
  metric: "cost",
  rows: [{ key: "/team/alice" }, { key: "", other: true, folded: 3 }],
  columns: [{ key: "2026-01" }, { key: "2026-02" }],
  cells: [
    { row: 0, column: 0, requests: 2, total_tokens: 100, total_cost: 1.5 },
    { row: 0, column: 1, requests: 1, total_tokens: 50, total_cost: null },
    { row: 1, column: 1, requests: 4, total_tokens: 400, total_cost: 2 },
  ],
};

test("pivotTable lays out cells with row, column and grand totals", () => {
  const table = pivotTable(BREAKDOWN, "cost");
  assert.deepEqual(table.grid, [
    [1.5, 0],
    [null, 2],
  ]);
  assert.deepEqual(table.rowTotals, [1.5, 2]);
  assert.deepEqual(table.columnTotals, [1.5, 2]);
  assert.equal(table.total, 3.5);
  assert.deepEqual(pivotTable(BREAKDOWN, "requests").rowTotals, [3, 4]);
  assert.equal(pivotTable(null, "cost").rows.length, 0);
});

test("pivotKeyLabel names other, blank and API key keys", () => {
  assert.equal(pivotKeyLabel("model", { key: "", other: true, folded: 3 }), "Other (3)");
  assert.equal(pivotKeyLabel("label", { key: "" }), "(none)");
  assert.equal(pivotKeyLabel("auth_key", { key: "k1" }, { k1: "CI key" }), "CI key");
  assert.equal(pivotKeyLabel("", { key: "" }), "Total");
  assert.equal(formatPivotValue(null, "cost"), "–");
  assert.equal(formatPivotValue(1.5, "cost"), "$1.50");
  assert.equal(formatPivotValue(0.004, "cost"), "$0.0040");
});

test("pivotCsv exports the table with totals and escapes fields", () => {
  const table = pivotTable(BREAKDOWN, "tokens");
  const csv = pivotCsv(table, {
    cornerLabel: "User path",
    rowLabels: ['/team/"a",x', "=SUM(A1)"],
    columnLabels: ["2026-01", "2026-02"],
  });
  assert.equal(
    csv,
    "User path,2026-01,2026-02,Total\r\n" +
      '"/team/""a"",x",100,50,150\r\n' +
      "'=SUM(A1),,400,400\r\n" +
      "Total,100,450,550\r\n",
  );
});

test("pivotChartConfig stacks rows per column, or plots row totals alone", () => {
  const colors = { text: "#000", grid: "#ccc" };
  const table = pivotTable(BREAKDOWN, "cost");
  const stacked = pivotChartConfig(colors, table, {
    rowLabels: ["/team/alice", "Other"],
    columnLabels: ["2026-01", "2026-02"],
    metric: "cost",
    hasColumns: true,
  });
  assert.deepEqual(stacked.data.labels, ["2026-01", "2026-02"]);
  assert.deepEqual(
    stacked.data.datasets.map((dataset) => dataset.data),
    [
      [1.5, 0],
      [0, 2],
    ],
  );
  assert.equal(stacked.options.scales.y.stacked, true);
  const single = pivotChartConfig(colors, table, { rowLabels: ["a", "b"], columnLabels: [], metric: "cost", hasColumns: false });
  assert.deepEqual(single.data.labels, ["a", "b"]);
  assert.deepEqual(single.data.datasets[0].data, [1.5, 2]);
});
