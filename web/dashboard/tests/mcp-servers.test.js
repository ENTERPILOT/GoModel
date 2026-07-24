// Pure-logic tests for the MCP Servers page, ported from the legacy
// internal/admin/dashboard/static/js/modules/mcp-servers.test.cjs. Fetch-flow
// and DOM/template cases are covered by the Svelte components and skipped.
import test from "node:test";
import assert from "node:assert/strict";

import {
  buildMcpServerPayload,
  defaultMcpCatalog,
  defaultMcpServerForm,
  deriveMcpServerSlug,
  filterMcpServers,
  mcpCatalogIsEmpty,
  mcpCatalogSections,
  mcpErrorPayloadMessage,
  mcpHeaderRowsToObject,
  mcpHeadersToRows,
  mcpServerEndpointLabel,
  mcpServerFormFromServer,
  mcpServerStatus,
  mcpServerStatusClass,
  mcpServerStatusTitle,
  normalizeMcpCatalog,
  normalizeMcpCommaList,
  normalizeMcpUserPaths,
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
  assert.deepEqual(normalizeMcpCommaList(" search_issues, get_file ,, "), [
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
      allowed_tools: "search_issues, get_file",
      disallowed_tools: "",
      user_paths: "/team/alpha\n/team/beta",
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
      allowed_tools: "search_issues",
      disallowed_tools: "delete_repo",
      user_paths: "/team/alpha",
      tool_timeout_seconds: "45",
    },
  );
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

test("mcpErrorPayloadMessage extracts admin error messages", () => {
  assert.equal(
    mcpErrorPayloadMessage({ error: { message: "storage unavailable" } }, "fallback"),
    "storage unavailable",
  );
  assert.equal(mcpErrorPayloadMessage(null, "fallback"), "fallback");
  assert.equal(mcpErrorPayloadMessage({ error: "flat" }, "fallback"), "fallback");
});
