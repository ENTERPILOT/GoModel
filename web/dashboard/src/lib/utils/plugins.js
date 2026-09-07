// Pure helpers over GET /admin/plugins rows:
//   { name, version, description, kinds: [phase...], mutates,
//     source: "builtin" | "/path/x.so", fields: [Field...],
//     route_fields: [Field...], health: "ok" | "error", error }
// Kept free of Svelte so node:test can import it; the plugins store owns the
// fetch.

import { routeSchemaFields } from "./schemaFields.js";

// normalizePlugins coerces the payload into rows with every field present so
// the UI never branches on missing keys.
export function normalizePlugins(items) {
  return (Array.isArray(items) ? items : [])
    .filter((item) => item && typeof item === "object" && !Array.isArray(item))
    .map((item) => ({
      name: String(item.name || "").trim(),
      version: String(item.version || "").trim(),
      description: String(item.description || "").trim(),
      kinds: Array.isArray(item.kinds)
        ? item.kinds.map((kind) => String(kind || "").trim()).filter(Boolean)
        : [],
      mutates: Boolean(item.mutates),
      source: String(item.source || "").trim(),
      fields: Array.isArray(item.fields) ? item.fields : [],
      // Older payloads carry route fields inside `fields` with scope "route".
      route_fields: Array.isArray(item.route_fields)
        ? item.route_fields
        : routeSchemaFields(item.fields),
      health: String(item.health || "ok").trim().toLowerCase() || "ok",
      error: String(item.error || "").trim(),
    }))
    .filter((plugin) => plugin.name);
}

export function pluginByName(plugins, name) {
  const wanted = String(name || "").trim();
  if (!wanted) return null;
  return (
    (Array.isArray(plugins) ? plugins : []).find(
      (plugin) => plugin && plugin.name === wanted,
    ) || null
  );
}

// pluginRouteFields lists the per-virtual-model fields of a routing plugin;
// [] when the plugin is unknown (endpoint unavailable, plugin unloaded).
export function pluginRouteFields(plugins, name) {
  const plugin = pluginByName(plugins, name);
  return plugin && Array.isArray(plugin.route_fields) ? plugin.route_fields : [];
}

export function pluginHealthy(plugin) {
  return Boolean(plugin) && plugin.health !== "error" && !plugin.error;
}

// pluginSourceIsBuiltin: "builtin" rows ship with the gateway; anything else
// is the path of a loaded .so (or another loader's identifier).
export function pluginSourceIsBuiltin(plugin) {
  return String((plugin && plugin.source) || "").trim().toLowerCase() === "builtin";
}
