// Request-type filter for the audit log. Each type groups one or more gateway
// operations (core.Operation); the server filters stored rows by operation,
// and auditTypeForPath mirrors internal/core/endpoint_operations.go so live
// rows can be filtered the same way before they are persisted.
// Pure logic with relative imports so node --test can load it directly.

import * as m from "../../lib/paraglide/messages.js";

export const AUDIT_TYPES = [
  { key: "chat", label: () => m.audit_type_chat(), operations: ["chat_completions"] },
  { key: "responses", label: () => m.audit_type_responses(), operations: ["responses", "conversations"] },
  { key: "embeddings", label: () => m.audit_type_embeddings(), operations: ["embeddings"] },
  {
    key: "audio",
    label: () => m.audit_type_audio(),
    operations: ["audio_speech", "audio_transcriptions", "audio_translations"],
  },
  { key: "images", label: () => m.audit_type_images(), operations: ["image_generations", "image_edits"] },
  { key: "batches", label: () => m.audit_type_batches(), operations: ["batches", "files"] },
  { key: "realtime", label: () => m.audit_type_realtime(), operations: ["realtime"] },
  { key: "passthrough", label: () => m.audit_type_passthrough(), operations: ["provider_passthrough"] },
  { key: "mcp", label: () => m.audit_type_mcp(), operations: ["mcp"] },
];

const TYPE_KEYS = new Set(AUDIT_TYPES.map((type) => type.key));

const EXACT_PATHS = {
  "/v1/chat/completions": "chat",
  "/v1/messages": "chat",
  "/v1/messages/count_tokens": "chat",
  "/v1/embeddings": "embeddings",
  "/v1/audio/speech": "audio",
  "/v1/audio/transcriptions": "audio",
  "/v1/audio/translations": "audio",
  "/v1/images/generations": "images",
  "/v1/images/edits": "images",
  "/v1/realtime": "realtime",
  "/v1/realtime/calls": "realtime",
  "/v1/realtime/client_secrets": "realtime",
  "/v1/realtime/translations": "realtime",
  "/v1/realtime/translations/calls": "realtime",
  "/v1/realtime/translations/client_secrets": "realtime",
};

const PATH_PREFIXES = [
  ["/v1/responses", "responses"],
  ["/v1/conversations", "responses"],
  ["/v1/messages/batches", "batches"],
  ["/v1/batches", "batches"],
  ["/v1/files", "batches"],
  ["/mcp", "mcp"],
  ["/p", "passthrough"],
];

// auditTypeForPath returns the type key of a request path, or "".
export function auditTypeForPath(path) {
  let clean = String(path || "").trim().split("?")[0];
  if (clean.length > 1 && clean.endsWith("/")) clean = clean.slice(0, -1);
  if (EXACT_PATHS[clean]) return EXACT_PATHS[clean];
  for (const [prefix, type] of PATH_PREFIXES) {
    if (clean === prefix || clean.startsWith(prefix + "/")) return type;
  }
  return "";
}

// normalizeHiddenTypes keeps known keys and never hides every type: an empty
// list would silently match nothing, so that choice resets to showing all.
export function normalizeHiddenTypes(hidden) {
  const keys = Array.isArray(hidden) ? hidden.filter((key) => TYPE_KEYS.has(key)) : [];
  const unique = [...new Set(keys)];
  return unique.length >= AUDIT_TYPES.length ? [] : unique;
}

// auditOperationsQuery renders the `operation` query value for the visible
// types, or "" when nothing is hidden.
export function auditOperationsQuery(hidden) {
  const hiddenSet = new Set(normalizeHiddenTypes(hidden));
  if (hiddenSet.size === 0) return "";
  return AUDIT_TYPES.filter((type) => !hiddenSet.has(type.key))
    .flatMap((type) => type.operations)
    .join(",");
}

// auditEntryTypeVisible reports whether a (live) entry passes the filter.
export function auditEntryTypeVisible(entry, hidden) {
  const hiddenSet = new Set(normalizeHiddenTypes(hidden));
  if (hiddenSet.size === 0) return true;
  const type = auditTypeForPath(entry && entry.path);
  return type !== "" && !hiddenSet.has(type);
}
