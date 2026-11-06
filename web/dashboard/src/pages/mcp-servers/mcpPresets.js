// Well-known hosted MCP servers offered when adding a server, so connecting
// one is "pick it, then sign in" instead of filling in URLs and headers.
// Only data lives here; the gateway treats a preset-created server like any
// other. auth lists the connection methods the server accepts, the first
// being the default. oauthClient "manual" marks servers whose authorization
// server has no dynamic client registration, so the operator registers an
// OAuth app first (tokenURL/clientURL point at the vendor's settings page).

import { mcpServerSlug } from "./mcp-servers.js";

export const MCP_AUTH_OAUTH = "oauth";
export const MCP_AUTH_TOKEN = "token";
export const MCP_AUTH_NONE = "none";
export const MCP_PRESET_CUSTOM = "custom";

export const MCP_SERVER_PRESETS = Object.freeze([
  {
    id: "linear",
    name: "Linear",
    url: "https://mcp.linear.app/mcp",
    auth: [MCP_AUTH_OAUTH, MCP_AUTH_TOKEN],
    tokenURL: "https://linear.app/settings/account/security",
  },
  {
    id: "notion",
    name: "Notion",
    url: "https://mcp.notion.com/mcp",
    auth: [MCP_AUTH_OAUTH],
  },
  {
    id: "github",
    name: "GitHub",
    url: "https://api.githubcopilot.com/mcp/",
    auth: [MCP_AUTH_OAUTH, MCP_AUTH_TOKEN],
    oauthClient: "manual",
    clientURL: "https://github.com/settings/applications/new",
    tokenURL: "https://github.com/settings/personal-access-tokens",
  },
  {
    id: "sentry",
    name: "Sentry",
    url: "https://mcp.sentry.dev/mcp",
    auth: [MCP_AUTH_OAUTH],
  },
  {
    id: "stripe",
    name: "Stripe",
    url: "https://mcp.stripe.com",
    auth: [MCP_AUTH_OAUTH, MCP_AUTH_TOKEN],
    tokenURL: "https://dashboard.stripe.com/apikeys",
  },
  {
    id: "supabase",
    name: "Supabase",
    url: "https://mcp.supabase.com/mcp",
    auth: [MCP_AUTH_OAUTH, MCP_AUTH_TOKEN],
    tokenURL: "https://supabase.com/dashboard/account/tokens",
  },
  {
    id: "vercel",
    name: "Vercel",
    url: "https://mcp.vercel.com",
    auth: [MCP_AUTH_OAUTH],
  },
  {
    id: "huggingface",
    name: "Hugging Face",
    url: "https://huggingface.co/mcp",
    auth: [MCP_AUTH_TOKEN, MCP_AUTH_NONE],
    tokenURL: "https://huggingface.co/settings/tokens",
  },
]);

export function mcpPresetById(id) {
  return MCP_SERVER_PRESETS.find((preset) => preset.id === id) || null;
}

// mcpPresetForURL finds the preset a saved server was created from, so the
// sign-in dialog can link to the vendor's OAuth app page. A trailing slash
// does not matter: config.yaml examples often drop it.
export function mcpPresetForURL(url) {
  const value = withoutTrailingSlash(url);
  if (!value) {
    return null;
  }
  return MCP_SERVER_PRESETS.find((preset) => withoutTrailingSlash(preset.url) === value) || null;
}

function withoutTrailingSlash(url) {
  return String(url || "")
    .trim()
    .replace(/\/+$/, "");
}

export function mcpPresetHost(preset) {
  return mcpURLHostName(preset.url);
}

// mcpFreeSlug returns base, or base-2, base-3, ... when a server or virtual
// server already uses it, so adding a second Linear workspace does not stop
// at a slug clash the operator cannot see in the preset form.
export function mcpFreeSlug(base, servers = [], virtualServers = []) {
  const taken = new Set([
    ...(servers || []).map((server) => mcpServerSlug(server)),
    ...(virtualServers || []).map((virtual) => String(virtual?.name || "")),
  ]);
  if (!taken.has(base)) {
    return base;
  }
  for (let n = 2; ; n++) {
    const slug = base + "-" + n;
    if (!taken.has(slug)) {
      return slug;
    }
  }
}

// mcpURLHostName is the name a custom server gets from its URL until the
// operator types one.
export function mcpURLHostName(url) {
  try {
    return new URL(String(url || "").trim()).host;
  } catch {
    return "";
  }
}

// mcpConnectionHeaders adds the token as an Authorization header unless the
// operator already set one under the advanced headers. Other auth methods
// leave the rows alone.
export function mcpConnectionHeaders(rows, auth, token) {
  const headers = (rows || []).map((row) => ({ ...row }));
  const value = String(token || "").trim();
  if (auth !== MCP_AUTH_TOKEN || !value) {
    return headers;
  }
  if (mcpHasAuthorizationHeader(headers)) {
    return headers;
  }
  const bearer = /^bearer\s/i.test(value) ? value : "Bearer " + value;
  return [...headers, { name: "Authorization", value: bearer }];
}

export function mcpHasAuthorizationHeader(rows) {
  return (rows || []).some(
    (row) =>
      String(row.name || "").trim().toLowerCase() === "authorization" &&
      String(row.value || "").trim() !== "",
  );
}
