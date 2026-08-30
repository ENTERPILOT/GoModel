// Pure logic for the Users page: the user-path tree (groups and users) and
// its per-node model allowlists, on top of GET/PUT/DELETE /admin/users.

import * as m from "../../lib/paraglide/messages.js";
import { parseModelSelectors } from "../../lib/utils/modelSelectors.js";

export function defaultUserForm() {
  return { user_path: "", allowed_models: [], description: "" };
}

// userPathDepth counts the segments of a canonical path ("/" is 0).
export function userPathDepth(userPath) {
  const value = String(userPath || "").trim();
  if (!value || value === "/") {
    return 0;
  }
  return value.split("/").filter(Boolean).length;
}

// userPathLeaf returns the last segment of a path ("/" for the root).
export function userPathLeaf(userPath) {
  const value = String(userPath || "").trim();
  if (!value || value === "/") {
    return "/";
  }
  const parts = value.split("/").filter(Boolean);
  return parts[parts.length - 1] || "/";
}

// userNodeRestricted reports whether a node's effective access is narrower
// than "everything": it has its own allowlist or inherits one.
export function userNodeRestricted(node) {
  if (!node) {
    return false;
  }
  return (node.allowed_models || []).length > 0 || (node.inherited_from || []).length > 0;
}

// userNodeKind labels a node for the tree: a node with keys bound directly to
// it is a user, a node that only contains other nodes is a group.
export function userNodeKind(node, nodes) {
  if (!node) {
    return "group";
  }
  if ((node.key_count || 0) > 0) {
    return "user";
  }
  const prefix = node.user_path === "/" ? "/" : node.user_path + "/";
  const hasChildren = (nodes || []).some(
    (other) => other.user_path !== node.user_path && other.user_path.startsWith(prefix),
  );
  return hasChildren ? "group" : "user";
}

// selectorMatchesModel mirrors the backend matcher for a typed selector
// against a qualified "provider/model" id: "*" matches all, "provider/*" or
// "provider/" the provider, "provider/model" exactly, and a bare name any
// provider's model of that name.
export function selectorMatchesModel(selector, qualifiedID) {
  const entry = String(selector || "").trim();
  const id = String(qualifiedID || "").trim();
  if (!entry || !id) {
    return false;
  }
  if (entry === "*" || entry === "/") {
    return true;
  }
  const slash = id.indexOf("/");
  const provider = slash === -1 ? "" : id.slice(0, slash);
  const model = slash === -1 ? id : id.slice(slash + 1);
  if (entry.endsWith("/*") || entry.endsWith("/")) {
    return provider !== "" && entry.replace(/\/\*?$/, "") === provider;
  }
  if (entry.includes("/")) {
    return entry === id;
  }
  return entry === model;
}

// parentEffectiveModels returns the effective model list of the nearest
// ancestor node present in the tree (the node itself excluded), or null when
// the tree has no ancestor with a computed list.
export function parentEffectiveModels(nodes, userPath) {
  const list = Array.isArray(nodes) ? nodes : [];
  const trimmed = String(userPath || "").trim();
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const segments = raw.split("/").filter(Boolean);
  for (let depth = segments.length - 1; depth >= 0; depth -= 1) {
    const ancestor = depth === 0 ? "/" : "/" + segments.slice(0, depth).join("/");
    const node = list.find((entry) => entry.user_path === ancestor);
    if (node && Array.isArray(node.effective_models)) {
      return node.effective_models;
    }
  }
  return null;
}

// previewEffectiveModels intersects a parent's effective models with the
// selectors being edited. An empty selector list leaves the parent's set.
export function previewEffectiveModels(parentModels, selectors) {
  const models = Array.isArray(parentModels) ? parentModels : [];
  const list = Array.isArray(selectors) ? selectors : [];
  if (!list.length) {
    return models;
  }
  return models.filter((id) => list.some((selector) => selectorMatchesModel(selector, id)));
}

// userSelectorOptions builds the quick-add picker from the shared model
// inventory: one provider-wide wildcard per provider, then every concrete
// model selector, both sorted. Options carry the SearchSelect shape.
export function userSelectorOptions(models) {
  const list = Array.isArray(models) ? models : [];
  const providers = [...new Set(list.map((entry) => entry?.provider_name).filter(Boolean))].sort();
  const selectors = [
    ...new Set(list.map((entry) => entry?.access?.selector || entry?.selector).filter(Boolean)),
  ].sort();
  return [
    ...providers.map((name) => ({
      value: name + "/*",
      label: name + "/*",
      description: m.users_quick_add_provider_all({ name }),
    })),
    ...selectors.map((selector) => ({ value: selector, label: selector, description: "" })),
  ];
}

// filterUserNodes applies the toolbar query against the path, description,
// and selectors.
export function filterUserNodes(nodes, query) {
  const needle = String(query || "").trim().toLowerCase();
  const list = Array.isArray(nodes) ? nodes : [];
  if (!needle) {
    return list;
  }
  return list.filter((node) =>
    [node.user_path, node.description, ...(node.allowed_models || [])]
      .filter(Boolean)
      .join(" ")
      .toLowerCase()
      .includes(needle),
  );
}

// sortUserNodes orders the tree depth-first by path so a group is followed
// by its descendants.
export function sortUserNodes(nodes) {
  const list = Array.isArray(nodes) ? nodes.slice() : [];
  return list.sort((a, b) => String(a.user_path).localeCompare(String(b.user_path)));
}

// userPathValidationError mirrors the backend's user path rules.
export function userPathValidationError(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return m.users_path_required();
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  for (const part of raw.split("/")) {
    const segment = part.trim();
    if (!segment) {
      continue;
    }
    if (segment === "." || segment === "..") {
      return m.users_path_segments();
    }
    if (segment.includes(":")) {
      return m.users_path_colon();
    }
  }
  return "";
}

// buildUpsertUserPayload validates the editor form and builds the PUT
// /admin/users payload. Returns { error } or { payload }.
export function buildUpsertUserPayload(form) {
  const source = form || {};
  const pathError = userPathValidationError(source.user_path);
  if (pathError) {
    return { error: pathError };
  }
  const trimmed = String(source.user_path || "").trim();
  return {
    payload: {
      user_path: trimmed.startsWith("/") ? trimmed : "/" + trimmed,
      allowed_models: parseModelSelectors(source.allowed_models),
      description: String(source.description || "").trim() || undefined,
    },
  };
}
