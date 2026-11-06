// Pure-logic tests for the MCP server presets offered when adding a server.
import test from "node:test";
import assert from "node:assert/strict";

import {
  MCP_AUTH_NONE,
  MCP_AUTH_OAUTH,
  MCP_AUTH_TOKEN,
  MCP_SERVER_PRESETS,
  mcpConnectionHeaders,
  mcpFreeSlug,
  mcpHasAuthorizationHeader,
  mcpPresetById,
  mcpPresetForURL,
  mcpPresetHost,
  mcpURLHostName,
  mcpWithoutAuthorizationHeaders,
} from "../src/pages/mcp-servers/mcpPresets.js";
import { buildMcpServerPayload, defaultMcpServerForm } from "../src/pages/mcp-servers/mcp-servers.js";

test("every preset is a valid HTTPS server with known connection methods", () => {
  const ids = new Set();
  for (const preset of MCP_SERVER_PRESETS) {
    assert.ok(!ids.has(preset.id), `duplicate preset id ${preset.id}`);
    ids.add(preset.id);
    assert.match(preset.id, /^[a-z0-9][a-z0-9_-]{0,63}$/);
    assert.equal(new URL(preset.url).protocol, "https:");
    assert.ok(mcpPresetHost(preset));
    assert.ok(preset.auth.length > 0);
    for (const auth of preset.auth) {
      assert.ok([MCP_AUTH_OAUTH, MCP_AUTH_TOKEN, MCP_AUTH_NONE].includes(auth), auth);
    }
    if (preset.oauthClient === "manual") {
      assert.ok(preset.auth.includes(MCP_AUTH_OAUTH));
      assert.equal(new URL(preset.clientURL).protocol, "https:");
    }
  }
});

test("a preset's URL and slug make a form the server payload accepts", () => {
  const preset = mcpPresetById("linear");
  const form = { ...defaultMcpServerForm(), name: preset.name, slug: preset.id, url: preset.url };
  const built = buildMcpServerPayload(form, "create", []);
  assert.equal(built.error, undefined);
  assert.equal(built.payload.slug, "linear");
  assert.equal(built.payload.url, "https://mcp.linear.app/mcp");
  assert.equal(built.payload.transport, "http");
});

test("mcpFreeSlug skips slugs servers and virtual servers already use", () => {
  assert.equal(mcpFreeSlug("linear"), "linear");
  assert.equal(mcpFreeSlug("linear", [{ slug: "linear" }]), "linear-2");
  assert.equal(
    mcpFreeSlug("linear", [{ slug: "linear" }, { name: "linear-2" }], [{ name: "linear-3" }]),
    "linear-4",
  );
});

test("mcpURLHostName names a custom server after its URL host", () => {
  assert.equal(mcpURLHostName(" https://mcp.example.com:8443/mcp "), "mcp.example.com:8443");
  assert.equal(mcpURLHostName("not a url"), "");
  assert.equal(mcpURLHostName(""), "");
});

test("mcpConnectionHeaders adds a bearer token only for the token method", () => {
  assert.deepEqual(mcpConnectionHeaders([], MCP_AUTH_TOKEN, " lin_api_x "), [
    { name: "Authorization", value: "Bearer lin_api_x" },
  ]);
  assert.deepEqual(mcpConnectionHeaders([], MCP_AUTH_TOKEN, "Bearer ghp_x"), [
    { name: "Authorization", value: "Bearer ghp_x" },
  ]);
  assert.deepEqual(mcpConnectionHeaders([], MCP_AUTH_OAUTH, "ignored"), []);
  assert.deepEqual(mcpConnectionHeaders([], MCP_AUTH_TOKEN, "  "), []);
});

test("mcpConnectionHeaders keeps an Authorization header set under Advanced", () => {
  const rows = [{ name: "authorization", value: "Token custom" }];
  const headers = mcpConnectionHeaders(rows, MCP_AUTH_TOKEN, "other");
  assert.deepEqual(headers, rows);
  assert.notEqual(headers, rows);
});

test("mcpHasAuthorizationHeader ignores an empty value", () => {
  assert.equal(mcpHasAuthorizationHeader([{ name: "Authorization", value: "" }]), false);
  assert.equal(mcpHasAuthorizationHeader([{ name: " AUTHORIZATION ", value: "Bearer x" }]), true);
  assert.equal(mcpHasAuthorizationHeader(undefined), false);
});

test("mcpPresetForURL matches a saved server's URL, ignoring a trailing slash", () => {
  assert.equal(mcpPresetForURL("https://api.githubcopilot.com/mcp/")?.id, "github");
  assert.equal(mcpPresetForURL("https://api.githubcopilot.com/mcp")?.id, "github");
  assert.equal(mcpPresetForURL(" https://mcp.notion.com/mcp ")?.id, "notion");
  assert.equal(mcpPresetForURL("https://mcp.notion.com/mcp/")?.id, "notion");
  assert.equal(mcpPresetForURL("https://example.com/mcp"), null);
  assert.equal(mcpPresetForURL("/"), null);
  assert.equal(mcpPresetForURL(undefined), null);
});

test("mcpWithoutAuthorizationHeaders drops only Authorization rows", () => {
  const rows = [
    { name: " authorization ", value: "Bearer lin_api_x" },
    { name: "X-Team", value: "alpha" },
    { name: "Authorization", value: "" },
  ];
  assert.deepEqual(mcpWithoutAuthorizationHeaders(rows), [{ name: "X-Team", value: "alpha" }]);
  assert.deepEqual(mcpWithoutAuthorizationHeaders(undefined), []);
});
