// Pure MCP-servers page logic. Everything here is side-effect free so the
// node:test suite can exercise it directly.

export function defaultMcpServerForm() {
  return {
    name: "",
    slug: "",
    url: "",
    transport: "http",
    description: "",
    enabled: true,
    headers: [],
    allowed_tools: "",
    disallowed_tools: "",
    user_paths: "",
    tool_timeout_seconds: "",
  };
}

export function defaultMcpCatalog() {
  return {
    server: "",
    status: "",
    instructions: "",
    tools: [],
    prompts: [],
    resources: [],
    templates: [],
  };
}

export function mcpServerSlug(server) {
  return String((server && (server.slug || server.name)) || "").trim();
}

export function mcpServerStatus(server) {
  return String((server && server.status) || "").trim() || "connecting";
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
    return "Connected since " + format(server.connected_at);
  }
  return "";
}

export function mcpServerEndpointLabel(server) {
  if (String((server && server.transport) || "") === "stdio") {
    return "local command";
  }
  return String((server && server.url) || "").trim() || "—";
}

export function mcpServerSubCountsLabel(server) {
  const prompts = Number((server && server.prompt_count) || 0);
  const resources = Number((server && server.resource_count) || 0);
  return prompts + " prompts · " + resources + " resources";
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

export function normalizeMcpCommaList(value) {
  return String(value || "")
    .split(",")
    .map((item) => item.trim())
    .filter((item) => item);
}

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
    allowed_tools: (Array.isArray(server.allowed_tools)
      ? server.allowed_tools
      : []
    ).join(", "),
    disallowed_tools: (Array.isArray(server.disallowed_tools)
      ? server.disallowed_tools
      : []
    ).join(", "),
    user_paths: (Array.isArray(server.user_paths) ? server.user_paths : []).join(
      "\n",
    ),
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
    return { error: "Name is required." };
  }
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(slug)) {
    return {
      error:
        "Slug must use 1–64 lowercase ASCII letters, numbers, hyphens, or underscores.",
    };
  }
  if (
    mode === "create" &&
    (servers || []).some((server) => mcpServerSlug(server) === slug)
  ) {
    return { error: 'Slug "' + slug + '" is already in use.' };
  }
  if (!url) {
    return { error: "URL is required." };
  }
  let toolTimeoutSeconds;
  const rawTimeout = String(form.tool_timeout_seconds || "").trim();
  if (rawTimeout !== "") {
    const parsed = Number(rawTimeout);
    if (!Number.isSafeInteger(parsed) || parsed < 0) {
      return {
        error: "Tool timeout must be a non-negative whole number of seconds.",
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
      allowed_tools: normalizeMcpCommaList(form.allowed_tools),
      disallowed_tools: normalizeMcpCommaList(form.disallowed_tools),
      user_paths: normalizeMcpUserPaths(form.user_paths),
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
  });
  const sections = [
    { key: "tools", title: "Tools", items: (source.tools || []).map(feature("tool")) },
    {
      key: "prompts",
      title: "Prompts",
      items: (source.prompts || []).map(feature("prompt")),
    },
    {
      key: "resources",
      title: "Resources",
      items: (source.resources || []).map((item) => ({
        key: "resource:" + String(item.uri || ""),
        name: String(item.uri || ""),
        aggregated: "",
        description: describe(item.name, item.description),
      })),
    },
    {
      key: "templates",
      title: "Resource templates",
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

// mcpErrorPayloadMessage extracts the admin error message: the MCP
// admin API reports failures as { error: { message } }.
export function mcpErrorPayloadMessage(data, fallback) {
  if (
    data &&
    typeof data === "object" &&
    data.error &&
    typeof data.error === "object" &&
    typeof data.error.message === "string" &&
    data.error.message
  ) {
    return data.error.message;
  }
  return fallback;
}
