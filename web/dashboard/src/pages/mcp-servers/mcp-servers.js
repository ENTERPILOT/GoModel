// Pure MCP-servers page logic. Everything here is side-effect free so the
// node:test suite can exercise it directly.

import * as m from "../../lib/paraglide/messages.js";

// Tool filter modes. "exclude" stores disallowed_tools: every tool is exposed
// except the listed ones, so tools the server adds later appear automatically.
// "allow" stores allowed_tools: only the listed tools are exposed, so new
// tools stay hidden until selected.
export const MCP_TOOL_MODE_EXCLUDE = "exclude";
export const MCP_TOOL_MODE_ALLOW = "allow";

export function defaultMcpServerForm() {
  return {
    name: "",
    slug: "",
    url: "",
    transport: "http",
    description: "",
    enabled: true,
    headers: [],
    tool_mode: MCP_TOOL_MODE_EXCLUDE,
    tool_names: [],
    user_paths: "",
    disallowed_user_paths: "",
    tool_timeout_seconds: "",
  };
}

export function defaultMcpCatalog() {
  return {
    server: "",
    status: "",
    instructions: "",
    tools: [],
    excluded_tools: [],
    prompts: [],
    resources: [],
    templates: [],
  };
}

// A freshly saved server is returned before its first dial finishes, so the
// list arrives as "connecting" and only the gateway's background connect
// settles it. Re-poll until nothing is pending; without this the row reads
// "connecting" until the operator reloads the page.
export const MCP_SERVERS_POLL_MS = 3000;

// A failing poll retries: the row it is waiting on would otherwise stay
// "connecting" forever after one blip, which is the bug the loop exists to
// fix. The budget is what stops a properly down gateway being polled for as
// long as the page stays open.
export const MCP_SERVERS_POLL_MAX_FAILURES = 3;

export function mcpServersNeedPolling(servers) {
  const list = Array.isArray(servers) ? servers : [];
  return list.some((server) => mcpServerStatus(server) === "connecting");
}

export function mcpPollShouldRetry(consecutiveFailures) {
  return Number(consecutiveFailures || 0) < MCP_SERVERS_POLL_MAX_FAILURES;
}

export function mcpServerSlug(server) {
  return String((server && (server.slug || server.name)) || "").trim();
}

export function mcpServerStatus(server) {
  return String((server && server.status) || "").trim() || "connecting";
}

export function mcpServerStatusLabel(server) {
  const labels = {
    connected: m.mcp_status_connected,
    degraded: m.mcp_status_degraded,
    connecting: m.mcp_status_connecting,
    disabled: m.mcp_status_disabled,
  };
  const status = mcpServerStatus(server);
  return labels[status]?.() || status;
}

export function mcpServerStatusClass(server) {
  switch (mcpServerStatus(server)) {
    case "connected":
      return "status-success";
    case "degraded":
      return String((server && server.last_error) || "").trim()
        ? "status-error"
        : "status-warning";
    case "connecting":
      return "status-neutral";
    default:
      // "disabled" and anything unexpected render gray.
      return "status-unknown";
  }
}

// formatTimestamp is injected so this module stays free of store imports.
export function mcpServerStatusTitle(server, formatTimestamp) {
  const status = mcpServerStatus(server);
  const lastError = String((server && server.last_error) || "").trim();
  if (lastError && status !== "connected") {
    return lastError;
  }
  if (status === "connected" && server && server.connected_at) {
    const format =
      typeof formatTimestamp === "function" ? formatTimestamp : String;
    return m.mcp_connected_since({ date: format(server.connected_at) });
  }
  return "";
}

export function mcpServerEndpointLabel(server) {
  if (String((server && server.transport) || "") === "stdio") {
    return m.mcp_local_command();
  }
  return String((server && server.url) || "").trim() || "—";
}

export function mcpServerSubCountsLabel(server) {
  const prompts = Number((server && server.prompt_count) || 0);
  const resources = Number((server && server.resource_count) || 0);
  return m.mcp_sub_counts({ prompts, resources });
}

export function deriveMcpServerSlug(name) {
  const normalized = String(name || "")
    .normalize("NFKD")
    .toLowerCase();
  const slug = normalized
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64)
    .replace(/-+$/g, "");
  if (slug) {
    return slug;
  }
  let hash = 2166136261;
  for (const character of normalized) {
    hash = Math.imul((hash ^ character.codePointAt(0)) >>> 0, 16777619) >>> 0;
  }
  return "mcp-" + hash.toString(16).padStart(8, "0");
}

import { splitCommaList } from "../../lib/utils/format.js";

export { splitCommaList };

export function normalizeMcpUserPaths(value) {
  return String(value || "")
    .split("\n")
    .map((item) => item.trim())
    .filter((item) => item);
}

export function mcpHeadersToRows(headers) {
  if (!headers || typeof headers !== "object" || Array.isArray(headers)) {
    return [];
  }
  return Object.keys(headers)
    .sort()
    .map((name) => ({ name, value: String(headers[name] || "") }));
}

export function mcpHeaderRowsToObject(rows) {
  const headers = {};
  (Array.isArray(rows) ? rows : []).forEach((row) => {
    const name = String((row && row.name) || "").trim();
    if (!name) {
      return;
    }
    headers[name] = String((row && row.value) || "");
  });
  return headers;
}

export function filterMcpServers(servers, filter) {
  const list = Array.isArray(servers) ? servers : [];
  if (!filter) {
    return list;
  }
  const needle = String(filter).toLowerCase();
  return list.filter((server) => {
    const fields = [
      server.name,
      server.slug,
      server.url,
      server.transport,
      server.description,
      server.status,
    ];
    return fields.some((value) =>
      String(value || "")
        .toLowerCase()
        .includes(needle),
    );
  });
}

// mcpServerFormFromServer prefills the editor form for an existing row.
// Masked secret header values ("***") flow through unchanged so an untouched
// save round-trips them and the backend preserves the stored secret.
export function mcpServerFormFromServer(server) {
  return {
    name: String(server.name || "").trim(),
    slug: mcpServerSlug(server),
    url: String(server.url || "").trim(),
    transport: server.transport === "sse" ? "sse" : "http",
    description: String(server.description || "").trim(),
    enabled: server.enabled !== false,
    headers: mcpHeadersToRows(server.headers),
    ...mcpToolFilterFromServer(server),
    user_paths: (Array.isArray(server.user_paths) ? server.user_paths : []).join(
      "\n",
    ),
    disallowed_user_paths: (Array.isArray(server.disallowed_user_paths)
      ? server.disallowed_user_paths
      : []
    ).join("\n"),
    tool_timeout_seconds: server.tool_timeout_seconds
      ? String(server.tool_timeout_seconds)
      : "",
  };
}

// buildMcpServerPayload validates the editor form and produces the PUT
// /admin/mcp-servers payload. Returns { error } on validation failure or
// { payload } when the form is valid.
export function buildMcpServerPayload(form, mode, servers) {
  const name = String(form.name || "").trim();
  const slug = String(form.slug || deriveMcpServerSlug(name))
    .trim()
    .toLowerCase();
  const url = String(form.url || "").trim();
  const transport = form.transport === "sse" ? "sse" : "http";
  if (!name) {
    return { error: m.mcp_name_required() };
  }
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(slug)) {
    return {
      error:
        m.mcp_slug_invalid(),
    };
  }
  if (
    mode === "create" &&
    (servers || []).some((server) => mcpServerSlug(server) === slug)
  ) {
    return { error: m.mcp_slug_in_use({ slug }) };
  }
  if (!url) {
    return { error: m.mcp_url_required() };
  }
  const toolNames = uniqueToolNames(form.tool_names);
  const allowMode = form.tool_mode === MCP_TOOL_MODE_ALLOW;
  if (allowMode && toolNames.length === 0) {
    // An empty allowed_tools list means "no restriction" on the gateway, the
    // opposite of what an operator who unchecked everything expects.
    return { error: m.mcp_tools_allow_empty() };
  }
  let toolTimeoutSeconds;
  const rawTimeout = String(form.tool_timeout_seconds || "").trim();
  if (rawTimeout !== "") {
    const parsed = Number(rawTimeout);
    if (!Number.isSafeInteger(parsed) || parsed < 0) {
      return {
        error: m.mcp_timeout_invalid(),
      };
    }
    toolTimeoutSeconds = parsed;
  }
  return {
    payload: {
      name,
      slug,
      url,
      transport,
      headers: mcpHeaderRowsToObject(form.headers),
      description: String(form.description || "").trim(),
      enabled: Boolean(form.enabled),
      allowed_tools: allowMode ? toolNames : [],
      disallowed_tools: allowMode ? [] : toolNames,
      user_paths: normalizeMcpUserPaths(form.user_paths),
      disallowed_user_paths: normalizeMcpUserPaths(form.disallowed_user_paths),
      tool_timeout_seconds: toolTimeoutSeconds,
    },
  };
}

export function normalizeMcpCatalog(name, payload) {
  const source =
    payload && typeof payload === "object" && !Array.isArray(payload)
      ? payload
      : {};
  const list = (value) =>
    (Array.isArray(value) ? value : []).filter(
      (item) => item && typeof item === "object",
    );
  return {
    server: String(source.server || name || "").trim(),
    status: String(source.status || "").trim(),
    instructions: String(source.instructions || "").trim(),
    tools: list(source.tools),
    excluded_tools: list(source.excluded_tools),
    prompts: list(source.prompts),
    resources: list(source.resources),
    templates: list(source.templates),
  };
}

// mcpNamespacedName is the aggregated /mcp endpoint form of one tool or
// prompt name; the catalog reports the upstream originals.
function mcpNamespacedName(catalog, name) {
  return String((catalog && catalog.server) || "") + "_" + String(name || "");
}

export function mcpCatalogSections(catalog) {
  const source = catalog || defaultMcpCatalog();
  const describe = (name, description) => {
    const label = String(name || "").trim();
    const copy = String(description || "").trim();
    if (label && copy) {
      return label + " — " + copy;
    }
    return copy || label;
  };
  const feature = (kind) => (item) => ({
    key: kind + ":" + String(item.name || ""),
    name: String(item.name || ""),
    aggregated: mcpNamespacedName(source, item.name),
    description: String(item.description || "").trim(),
    readOnly: item.read_only === true,
    destructive: item.destructive === true,
  });
  const sections = [
    { key: "tools", title: m.mcp_catalog_tools(), items: (source.tools || []).map(feature("tool")) },
    {
      key: "excluded_tools",
      title: m.mcp_catalog_excluded_tools(),
      hint: m.mcp_catalog_excluded_tools_hint(),
      excluded: true,
      items: (source.excluded_tools || []).map((item) => ({
        ...feature("excluded")(item),
        aggregated: "",
      })),
    },
    {
      key: "prompts",
      title: m.mcp_catalog_prompts(),
      items: (source.prompts || []).map(feature("prompt")),
    },
    {
      key: "resources",
      title: m.mcp_catalog_resources(),
      items: (source.resources || []).map((item) => ({
        key: "resource:" + String(item.uri || ""),
        name: String(item.uri || ""),
        aggregated: "",
        description: describe(item.name, item.description),
      })),
    },
    {
      key: "templates",
      title: m.mcp_catalog_templates(),
      items: (source.templates || []).map((item) => ({
        key: "template:" + String(item.uri_template || ""),
        name: String(item.uri_template || ""),
        aggregated: "",
        description: describe(item.name, item.description),
      })),
    },
  ];
  return sections.filter((section) => section.items.length > 0);
}

export function mcpCatalogIsEmpty(catalog) {
  return mcpCatalogSections(catalog).length === 0;
}

// --- tool picker -----------------------------------------------------------

function uniqueToolNames(names) {
  const seen = new Set();
  const result = [];
  (Array.isArray(names) ? names : []).forEach((value) => {
    const name = String(value || "").trim();
    if (name && !seen.has(name)) {
      seen.add(name);
      result.push(name);
    }
  });
  return result;
}

// mcpToolFilterFromServer maps the stored allow/deny lists onto one editor
// mode. Config accepts both lists at once (deny applies after allow); the
// allow mode with allowed − disallowed exposes exactly the same tools.
export function mcpToolFilterFromServer(server) {
  const allowed = uniqueToolNames(server && server.allowed_tools);
  const disallowed = uniqueToolNames(server && server.disallowed_tools);
  if (allowed.length > 0) {
    return {
      tool_mode: MCP_TOOL_MODE_ALLOW,
      tool_names: allowed.filter((name) => !disallowed.includes(name)),
    };
  }
  return { tool_mode: MCP_TOOL_MODE_EXCLUDE, tool_names: disallowed };
}

// mcpDiscoveredTools merges the exposed and excluded catalog lists into one
// name-sorted list: the full set the upstream reports, whatever the filters.
export function mcpDiscoveredTools(catalog) {
  const source = catalog || {};
  const byName = new Map();
  [...(source.tools || []), ...(source.excluded_tools || [])].forEach((item) => {
    const name = String((item && item.name) || "").trim();
    if (name && !byName.has(name)) {
      byName.set(name, {
        name,
        description: String(item.description || "").trim(),
        readOnly: item.read_only === true,
        destructive: item.destructive === true,
      });
    }
  });
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

export function mcpToolExposed(form, name) {
  const listed = (form.tool_names || []).includes(name);
  return form.tool_mode === MCP_TOOL_MODE_ALLOW ? listed : !listed;
}

// setMcpToolsExposed returns the tool_names list after exposing or hiding
// every given tool in the form's current mode.
export function setMcpToolsExposed(form, names, exposed) {
  const targets = uniqueToolNames(names);
  const listed = form.tool_mode === MCP_TOOL_MODE_ALLOW ? exposed : !exposed;
  const current = uniqueToolNames(form.tool_names);
  if (listed) {
    return uniqueToolNames([...current, ...targets]);
  }
  return current.filter((name) => !targets.includes(name));
}

// mcpToolModeSwitchable reports whether the mode can flip without changing
// what is exposed. Converting between an allowlist and a denylist needs the
// full tool set; without it, an allowlist would become an empty denylist,
// which exposes every tool. An empty list is safe to flip either way.
export function mcpToolModeSwitchable(form, discovered) {
  return (discovered || []).length > 0 || uniqueToolNames(form.tool_names).length === 0;
}

// switchMcpToolMode flips the filter mode while keeping every discovered
// tool's exposure unchanged; only the treatment of future tools changes.
// Names the server does not report are meaningful only in the old mode.
// When the flip is not safe (see mcpToolModeSwitchable) the form is kept.
export function switchMcpToolMode(form, mode, discovered) {
  const next = mode === MCP_TOOL_MODE_ALLOW ? MCP_TOOL_MODE_ALLOW : MCP_TOOL_MODE_EXCLUDE;
  if (next === form.tool_mode || !mcpToolModeSwitchable(form, discovered)) {
    return { tool_mode: form.tool_mode, tool_names: uniqueToolNames(form.tool_names) };
  }
  const names = (discovered || []).map((tool) => tool.name);
  const exposed = names.filter((name) => mcpToolExposed(form, name));
  return {
    tool_mode: next,
    tool_names:
      next === MCP_TOOL_MODE_ALLOW
        ? exposed
        : names.filter((name) => !exposed.includes(name)),
  };
}

// mcpToolPickerRows lists every discovered tool with its exposure in the
// form, followed by listed names the server does not report (typos, removed
// tools, or names added before the server connected). query narrows by name
// or description.
export function mcpToolPickerRows(form, discovered, query) {
  const needle = String(query || "").trim().toLowerCase();
  const known = new Set((discovered || []).map((tool) => tool.name));
  const rows = (discovered || []).map((tool) => ({
    ...tool,
    exposed: mcpToolExposed(form, tool.name),
    missing: false,
  }));
  uniqueToolNames(form.tool_names).forEach((name) => {
    if (!known.has(name)) {
      rows.push({
        name,
        description: "",
        readOnly: false,
        destructive: false,
        exposed: form.tool_mode === MCP_TOOL_MODE_ALLOW,
        missing: true,
      });
    }
  });
  if (!needle) {
    return rows;
  }
  return rows.filter(
    (row) =>
      row.name.toLowerCase().includes(needle) ||
      row.description.toLowerCase().includes(needle),
  );
}

// mcpToolSelectionSummary counts discovered tools only: a listed name the
// server does not report is neither exposed nor hidden today.
export function mcpToolSelectionSummary(form, discovered) {
  const total = (discovered || []).length;
  const exposed = (discovered || []).filter((tool) =>
    mcpToolExposed(form, tool.name),
  ).length;
  return { exposed, total };
}
