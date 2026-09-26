// Pure-logic tests for the MCP Servers page, ported from the legacy
// internal/admin/dashboard/static/js/modules/mcp-servers.test.cjs. Fetch-flow
// and DOM/template cases are covered by the Svelte components and skipped.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  buildMcpServerPayload,
  defaultMcpCatalog,
  defaultMcpServerForm,
  deriveMcpServerSlug,
  filterMcpServers,
  mcpCatalogIsEmpty,
  mcpCatalogSections,
  mcpHeaderRowsToObject,
  mcpHeadersToRows,
  mcpServerEndpointLabel,
  mcpServerFormFromServer,
  mcpPollShouldRetry,
  MCP_SERVERS_POLL_MAX_FAILURES,
  mcpServersNeedPolling,
  mcpServerStatus,
  mcpServerStatusClass,
  mcpServerStatusTitle,
  normalizeMcpCatalog,
  splitCommaList,
  normalizeMcpUserPaths,
  mcpDiscoveredTools,
  mcpToolFilterFromServer,
  mcpToolModeSwitchable,
  mcpToolPickerRows,
  mcpToolSelectionSummary,
  setMcpToolsExposed,
  switchMcpToolMode,
} from "../src/pages/mcp-servers/mcp-servers.js";

test("deriveMcpServerSlug normalizes display names and falls back to a hash", () => {
  assert.equal(deriveMcpServerSlug("  Linear MCP  "), "linear-mcp");
  assert.equal(deriveMcpServerSlug("Café Tools"), "cafe-tools");
  assert.equal(deriveMcpServerSlug("线性"), "mcp-b7ccbb8b");
});

test("mcpHeadersToRows and mcpHeaderRowsToObject round-trip, preserving masked secrets", () => {
  const rows = mcpHeadersToRows({ "X-Extra": "plain", Authorization: "***" });
  assert.deepEqual(rows, [
    { name: "Authorization", value: "***" },
    { name: "X-Extra", value: "plain" },
  ]);

  rows.push({ name: "  ", value: "dropped: empty name" });
  rows.push({ name: "X-New", value: "fresh-secret" });
  assert.deepEqual(mcpHeaderRowsToObject(rows), {
    Authorization: "***",
    "X-Extra": "plain",
    "X-New": "fresh-secret",
  });
});

test("mcpHeadersToRows tolerates missing or malformed header payloads", () => {
  assert.deepEqual(mcpHeadersToRows(null), []);
  assert.deepEqual(mcpHeadersToRows(["not", "an", "object"]), []);
});

test("mcpServerStatusClass maps statuses to badge classes", () => {
  assert.equal(mcpServerStatusClass({ status: "connected" }), "status-success");
  assert.equal(
    mcpServerStatusClass({ status: "degraded", last_error: "boom" }),
    "status-error",
  );
  assert.equal(mcpServerStatusClass({ status: "degraded" }), "status-warning");
  assert.equal(mcpServerStatusClass({ status: "connecting" }), "status-neutral");
  assert.equal(mcpServerStatusClass({ status: "disabled" }), "status-unknown");
  assert.equal(mcpServerStatusClass({}), "status-neutral");
});

test("mcpServerStatus defaults to connecting", () => {
  assert.equal(mcpServerStatus({}), "connecting");
  assert.equal(mcpServerStatus({ status: " degraded " }), "degraded");
});

test("mcpServersNeedPolling stays on only while a server is still connecting", () => {
  assert.equal(mcpServersNeedPolling([{ name: "a", status: "connecting" }]), true);
  // A server saved a moment ago comes back without a status yet.
  assert.equal(mcpServersNeedPolling([{ name: "a" }]), true);
  assert.equal(
    mcpServersNeedPolling([
      { name: "a", status: "connected" },
      { name: "b", status: "connecting" },
    ]),
    true,
  );
  // Degraded is terminal for the poll loop: the row already shows last_error
  // and the gateway re-probes it on its own schedule.
  assert.equal(
    mcpServersNeedPolling([
      { name: "a", status: "connected" },
      { name: "b", status: "degraded" },
      { name: "c", status: "disabled" },
    ]),
    false,
  );
  assert.equal(mcpServersNeedPolling([]), false);
  assert.equal(mcpServersNeedPolling(null), false);
});

test("mcpPollShouldRetry keeps retrying a failing poll until the budget runs out", () => {
  assert.equal(mcpPollShouldRetry(0), true);
  assert.equal(mcpPollShouldRetry(MCP_SERVERS_POLL_MAX_FAILURES - 1), true);
  assert.equal(mcpPollShouldRetry(MCP_SERVERS_POLL_MAX_FAILURES), false);
  assert.equal(mcpPollShouldRetry(undefined), true);
});

// Guard for the poll loop's lifecycle contract (#1024). The store uses runes,
// so it cannot be imported here; like editor-dialog.test.js, this asserts the
// wiring on the source. Both halves are regressions the loop shipped with:
// a failed background poll that never retried left the row it was waiting on
// stuck on "connecting", and a response landing after the page was left
// scheduled a timer that outlived it.
test("the MCP connect poll retries failures and cannot outlive the page", () => {
  const SRC = fileURLToPath(new URL("../src", import.meta.url));
  const store = readFileSync(
    join(SRC, "pages/mcp-servers/mcpServers.svelte.js"),
    "utf8",
  );
  const fetchServersDecl = (
    store.match(
      /async fetchServers\(\{ background = false \} = \{\}\) \{[\s\S]*?\n  \}/,
    ) || [""]
  )[0];

  // Every scheduling path passes the generation captured when the request
  // started, and the timer is only armed while that generation is current.
  const schedule = store.match(/#schedulePoll\(generation\) \{[\s\S]*?\n  \}/);
  assert.ok(schedule, "#schedulePoll(generation) missing");
  assert.ok(
    schedule[0].indexOf("generation !== this.#pollGeneration") <
      schedule[0].indexOf("this.#clearPoll()"),
    "a stale generation must return before clearing a newer timer",
  );
  assert.match(schedule[0], /setTimeout\(/);
  assert.equal(
    store.match(/this\.#schedulePoll\((?!generation\))/),
    null,
    "#schedulePoll must always be called with the request's generation",
  );

  // The generation is captured before the first await, so a cleanup during
  // the shared runtime-config load retires the request too.
  assert.ok(fetchServersDecl, "fetchServers declaration missing");
  const capture = fetchServersDecl.indexOf(
    "const generation = this.#pollGeneration;",
  );
  const firstAwait = fetchServersDecl.indexOf("await ");
  assert.ok(capture >= 0, "fetchServers must capture the poll generation");
  assert.ok(
    capture < firstAwait,
    "the generation must be captured before the first await",
  );
  assert.match(
    fetchServersDecl.slice(firstAwait),
    /^await runtimeConfig\.ensureLoaded\(\);\s*\n\s*if \(generation !== this\.#pollGeneration\) \{\s*\n\s*return;/,
  );

  // Overlapping list requests: the newest one wins. The sequence is taken
  // before the first await and checked before any response is applied, so a
  // slow poll cannot restore a list a delete just removed.
  const seqCapture = fetchServersDecl.indexOf("const seq = ++this.#listSeq;");
  assert.ok(seqCapture >= 0, "fetchServers must take a list sequence");
  assert.ok(
    seqCapture < firstAwait,
    "the list sequence must be taken before the first await",
  );
  const seqCheck = fetchServersDecl.indexOf("seq !== this.#listSeq");
  assert.ok(seqCheck >= 0, "a superseded response must be dropped");
  assert.ok(
    seqCheck < fetchServersDecl.indexOf("this.servers = outcome.items;"),
    "the sequence check must run before the response is applied",
  );
  assert.match(fetchServersDecl, /if \(!background && seq === this\.#listSeq\)/);

  // Leaving the page invalidates whatever is in flight.
  const stop = store.match(/stopPolling\(\) \{[\s\S]*?\n  \}/);
  assert.ok(stop, "stopPolling missing");
  assert.match(stop[0], /this\.#pollGeneration \+= 1;/);
  assert.match(stop[0], /this\.#clearPoll\(\);/);

  // A failed background poll retries on the budget instead of giving up.
  const backgroundFailure = fetchServersDecl.match(
    /if \(background\) \{[\s\S]*?\n        \}/,
  );
  assert.ok(backgroundFailure, "background failure branch missing");
  assert.match(backgroundFailure[0], /this\.#pollFailures \+= 1;/);
  assert.match(
    backgroundFailure[0],
    /mcpPollShouldRetry\(this\.#pollFailures\)[\s\S]*?this\.#schedulePoll\(generation\)/,
  );
  assert.equal(
    backgroundFailure[0].includes("this.servers = []"),
    false,
    "a failed background poll must keep the list it already has",
  );
});

test("mcpServerStatusTitle surfaces last_error for degraded servers", () => {
  assert.equal(
    mcpServerStatusTitle({ status: "degraded", last_error: "dial tcp: refused" }),
    "dial tcp: refused",
  );
  assert.equal(
    mcpServerStatusTitle(
      { status: "connected", connected_at: "2026-07-07T00:00:00Z" },
      (ts) => "formatted:" + ts,
    ),
    "Connected since formatted:2026-07-07T00:00:00Z",
  );
  assert.equal(mcpServerStatusTitle({ status: "connecting" }), "");
});

test("mcpServerEndpointLabel shows a command indicator for stdio servers", () => {
  assert.equal(mcpServerEndpointLabel({ transport: "stdio", url: "" }), "local command");
  assert.equal(
    mcpServerEndpointLabel({ transport: "http", url: "https://mcp.example.com/mcp" }),
    "https://mcp.example.com/mcp",
  );
  assert.equal(mcpServerEndpointLabel({ transport: "http" }), "—");
});

test("list normalization splits comma tools and newline user paths", () => {
  assert.deepEqual(splitCommaList(" search_issues, get_file ,, "), [
    "search_issues",
    "get_file",
  ]);
  assert.deepEqual(normalizeMcpUserPaths("/\n /team/alpha \n\n"), ["/", "/team/alpha"]);
});

test("buildMcpServerPayload produces the normalized PUT payload", () => {
  const built = buildMcpServerPayload(
    {
      name: " github ",
      slug: "github",
      url: " https://mcp.example.com/mcp ",
      transport: "sse",
      description: " Issue tools ",
      enabled: true,
      headers: [
        { name: "Authorization", value: "***" },
        { name: "", value: "ignored" },
      ],
      tool_mode: "allow",
      tool_names: ["search_issues", " get_file ", "search_issues"],
      user_paths: "/team/alpha\n/team/beta",
      disallowed_user_paths: " /team/alpha/contractors \n\n",
      tool_timeout_seconds: "45",
    },
    "edit",
    [],
  );

  assert.equal(built.error, undefined);
  assert.deepEqual(built.payload, {
    name: "github",
    slug: "github",
    url: "https://mcp.example.com/mcp",
    transport: "sse",
    headers: { Authorization: "***" },
    description: "Issue tools",
    enabled: true,
    allowed_tools: ["search_issues", "get_file"],
    disallowed_tools: [],
    user_paths: ["/team/alpha", "/team/beta"],
    disallowed_user_paths: ["/team/alpha/contractors"],
    tool_timeout_seconds: 45,
  });
});

test("buildMcpServerPayload validates required fields and timeout", () => {
  assert.equal(
    buildMcpServerPayload(defaultMcpServerForm(), "create", []).error,
    "Name is required.",
  );
  assert.equal(
    buildMcpServerPayload({ ...defaultMcpServerForm(), name: "github" }, "create", [])
      .error,
    "URL is required.",
  );
  assert.equal(
    buildMcpServerPayload(
      { ...defaultMcpServerForm(), name: "github", slug: "Bad Slug!" },
      "create",
      [],
    ).error,
    "Slug must use 1–64 lowercase ASCII letters, numbers, hyphens, or underscores.",
  );
  for (const rawTimeout of ["-3", "1.5"]) {
    assert.equal(
      buildMcpServerPayload(
        {
          ...defaultMcpServerForm(),
          name: "github",
          url: "https://mcp.example.com/mcp",
          tool_timeout_seconds: rawTimeout,
        },
        "create",
        [],
      ).error,
      "Tool timeout must be a non-negative whole number of seconds.",
    );
  }
});

test("buildMcpServerPayload maps the tool mode onto one filter list", () => {
  const base = {
    ...defaultMcpServerForm(),
    name: "github",
    url: "https://mcp.example.com/mcp",
  };

  const excluded = buildMcpServerPayload(
    { ...base, tool_names: ["delete_repo"] },
    "create",
    [],
  );
  assert.deepEqual(excluded.payload.allowed_tools, []);
  assert.deepEqual(excluded.payload.disallowed_tools, ["delete_repo"]);

  // An empty allowlist would expose every tool on the gateway, so the
  // "only selected" mode requires at least one selection.
  assert.equal(
    buildMcpServerPayload({ ...base, tool_mode: "allow", tool_names: [] }, "create", [])
      .error,
    "Select at least one tool, or switch new tools to exposed.",
  );
});

test("buildMcpServerPayload rejects duplicate slugs only when creating", () => {
  const form = {
    ...defaultMcpServerForm(),
    name: "GitHub",
    slug: "github",
    url: "https://mcp.example.com/mcp",
  };
  const servers = [{ name: "github" }];
  assert.equal(
    buildMcpServerPayload(form, "create", servers).error,
    'Slug "github" is already in use.',
  );
  assert.equal(buildMcpServerPayload(form, "edit", servers).error, undefined);
});

test("mcpServerFormFromServer prefills the editor form", () => {
  assert.deepEqual(
    mcpServerFormFromServer({
      name: "github",
      url: "https://mcp.example.com/mcp",
      transport: "sse",
      description: "Issue tools",
      enabled: false,
      managed: false,
      headers: { Authorization: "***" },
      allowed_tools: ["search_issues"],
      disallowed_tools: ["delete_repo"],
      user_paths: ["/team/alpha"],
      disallowed_user_paths: ["/team/alpha/contractors"],
      tool_timeout_seconds: 45,
    }),
    {
      name: "github",
      slug: "github",
      url: "https://mcp.example.com/mcp",
      transport: "sse",
      description: "Issue tools",
      enabled: false,
      headers: [{ name: "Authorization", value: "***" }],
      tool_mode: "allow",
      tool_names: ["search_issues"],
      user_paths: "/team/alpha",
      disallowed_user_paths: "/team/alpha/contractors",
      tool_timeout_seconds: "45",
    },
  );
});

test("mcpToolFilterFromServer picks one mode without changing exposure", () => {
  assert.deepEqual(mcpToolFilterFromServer({}), { tool_mode: "exclude", tool_names: [] });
  assert.deepEqual(mcpToolFilterFromServer({ disallowed_tools: ["delete_repo"] }), {
    tool_mode: "exclude",
    tool_names: ["delete_repo"],
  });
  // Deny applies after allow on the gateway, so allowed − disallowed exposes
  // the same tools.
  assert.deepEqual(
    mcpToolFilterFromServer({
      allowed_tools: ["read", "write"],
      disallowed_tools: ["write"],
    }),
    { tool_mode: "allow", tool_names: ["read"] },
  );
});

test("mcpDiscoveredTools merges exposed and excluded tools by name", () => {
  const discovered = mcpDiscoveredTools(
    normalizeMcpCatalog("github", {
      tools: [{ name: "search", read_only: true }],
      excluded_tools: [{ name: "delete_repo", description: "Delete", destructive: true }],
    }),
  );
  assert.deepEqual(discovered, [
    { name: "delete_repo", description: "Delete", readOnly: false, destructive: true },
    { name: "search", description: "", readOnly: true, destructive: false },
  ]);
});

test("tool picker toggles, bulk actions, and summary follow the mode", () => {
  const discovered = [{ name: "a" }, { name: "b" }, { name: "c" }].map((tool) => ({
    ...tool,
    description: "",
    readOnly: false,
    destructive: false,
  }));

  const exclude = { tool_mode: "exclude", tool_names: [] };
  exclude.tool_names = setMcpToolsExposed(exclude, ["b"], false);
  assert.deepEqual(exclude.tool_names, ["b"]);
  assert.deepEqual(mcpToolSelectionSummary(exclude, discovered), { exposed: 2, total: 3 });
  assert.deepEqual(setMcpToolsExposed(exclude, ["a", "b", "c"], true), []);
  assert.deepEqual(setMcpToolsExposed(exclude, ["a", "b", "c"], false), ["b", "a", "c"]);

  const allow = { tool_mode: "allow", tool_names: ["a"] };
  assert.deepEqual(setMcpToolsExposed(allow, ["c"], true), ["a", "c"]);
  assert.deepEqual(setMcpToolsExposed(allow, ["a"], false), []);
  assert.deepEqual(mcpToolSelectionSummary(allow, discovered), { exposed: 1, total: 3 });
});

test("switchMcpToolMode keeps every discovered tool's exposure", () => {
  const discovered = [{ name: "a" }, { name: "b" }, { name: "c" }];
  const toAllow = switchMcpToolMode(
    { tool_mode: "exclude", tool_names: ["b", "ghost"] },
    "allow",
    discovered,
  );
  assert.deepEqual(toAllow, { tool_mode: "allow", tool_names: ["a", "c"] });

  const back = switchMcpToolMode(toAllow, "exclude", discovered);
  assert.deepEqual(back, { tool_mode: "exclude", tool_names: ["b"] });
});

test("switchMcpToolMode keeps the list when the catalog is unknown", () => {
  // Without the full tool set, an allowlist would become an empty denylist,
  // which exposes every tool on save.
  const allow = { tool_mode: "allow", tool_names: ["read"] };
  assert.equal(mcpToolModeSwitchable(allow, []), false);
  assert.deepEqual(switchMcpToolMode(allow, "exclude", []), {
    tool_mode: "allow",
    tool_names: ["read"],
  });

  // An empty list flips safely, so a brand-new server can still pick a mode.
  const fresh = { tool_mode: "exclude", tool_names: [] };
  assert.equal(mcpToolModeSwitchable(fresh, []), true);
  assert.deepEqual(switchMcpToolMode(fresh, "allow", []), {
    tool_mode: "allow",
    tool_names: [],
  });
});

test("mcpToolPickerRows flags listed names the server does not report and filters", () => {
  const discovered = [
    { name: "create_issue", description: "Create an issue", readOnly: false, destructive: false },
    { name: "delete_repo", description: "", readOnly: false, destructive: true },
  ];
  const form = { tool_mode: "exclude", tool_names: ["delete_repo", "delet_repo"] };

  const rows = mcpToolPickerRows(form, discovered, "");
  assert.deepEqual(
    rows.map((row) => [row.name, row.exposed, row.missing]),
    [
      ["create_issue", true, false],
      ["delete_repo", false, false],
      ["delet_repo", false, true],
    ],
  );
  assert.deepEqual(
    mcpToolPickerRows(form, discovered, "ISSUE").map((row) => row.name),
    ["create_issue"],
  );
});

test("mcpCatalogSections lists excluded tools without an aggregated name", () => {
  const sections = mcpCatalogSections(
    normalizeMcpCatalog("github", {
      tools: [{ name: "search" }],
      excluded_tools: [{ name: "delete_repo", destructive: true }],
    }),
  );
  assert.deepEqual(
    sections.map((section) => section.key),
    ["tools", "excluded_tools"],
  );
  assert.equal(sections[1].excluded, true);
  assert.equal(sections[1].items[0].aggregated, "");
  assert.equal(sections[1].items[0].destructive, true);
});

test("mcpCatalogSections derives aggregated /mcp names for tools and prompts only", () => {
  const catalog = normalizeMcpCatalog("github", {
    server: "github",
    status: "connected",
    instructions: "Use the issue tools first.",
    tools: [
      { name: "create_issue", description: "Create a GitHub issue" },
      { name: "search_issues" },
    ],
    prompts: [{ name: "triage", description: "Triage an issue" }],
    resources: [{ uri: "repo://readme", name: "readme", description: "Repository readme" }],
    templates: [{ uri_template: "repo://{path}" }],
  });

  assert.equal(catalog.server, "github");
  assert.equal(catalog.instructions, "Use the issue tools first.");
  assert.equal(mcpCatalogIsEmpty(catalog), false);

  const sections = mcpCatalogSections(catalog);
  assert.deepEqual(
    sections.map((section) => section.key),
    ["tools", "prompts", "resources", "templates"],
  );

  const tools = sections[0].items;
  assert.deepEqual(tools[0], {
    key: "tool:create_issue",
    name: "create_issue",
    aggregated: "github_create_issue",
    description: "Create a GitHub issue",
    readOnly: false,
    destructive: false,
  });
  assert.equal(tools[1].aggregated, "github_search_issues");
  assert.equal(tools[1].description, "");

  assert.equal(sections[1].items[0].aggregated, "github_triage");

  // Resources and templates keep their URIs; only tools and prompts are
  // namespaced on the aggregated endpoint.
  assert.deepEqual(sections[2].items[0], {
    key: "resource:repo://readme",
    name: "repo://readme",
    aggregated: "",
    description: "readme — Repository readme",
  });
  assert.deepEqual(sections[3].items[0], {
    key: "template:repo://{path}",
    name: "repo://{path}",
    aggregated: "",
    description: "",
  });
});

test("empty catalog reports the empty hint state", () => {
  const catalog = normalizeMcpCatalog("github", {
    server: "github",
    status: "connecting",
    tools: [],
    prompts: [],
    resources: [],
    templates: [],
  });
  assert.equal(mcpCatalogSections(catalog).length, 0);
  assert.equal(mcpCatalogIsEmpty(catalog), true);
  assert.equal(mcpCatalogIsEmpty(defaultMcpCatalog()), true);
});

test("normalizeMcpCatalog tolerates missing lists and malformed payloads", () => {
  const fromNull = normalizeMcpCatalog("github", null);
  assert.equal(fromNull.server, "github");
  assert.deepEqual(fromNull.tools, []);
  assert.deepEqual(fromNull.templates, []);

  const sparse = normalizeMcpCatalog("github", {
    status: "degraded",
    tools: [{ name: "ok" }, "not-an-object", null],
  });
  assert.equal(sparse.status, "degraded");
  assert.deepEqual(sparse.tools, [{ name: "ok" }]);
  assert.deepEqual(sparse.prompts, []);
});

test("filterMcpServers matches name, url, transport, and status", () => {
  const servers = [
    {
      name: "github",
      url: "https://mcp.github.com",
      transport: "http",
      status: "connected",
    },
    {
      name: "search",
      url: "https://mcp.example.com",
      transport: "sse",
      status: "degraded",
    },
  ];

  assert.deepEqual(
    filterMcpServers(servers, "sse").map((server) => server.name),
    ["search"],
  );
  assert.deepEqual(
    filterMcpServers(servers, "github").map((server) => server.name),
    ["github"],
  );
  assert.equal(filterMcpServers(servers, "").length, 2);
});
