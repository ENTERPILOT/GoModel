// Column visibility for the request-log rows. Each column is one field group of
// the collapsed row (RequestEntrySummary); the expander, session-usage and
// Interactions controls are not columns and always render. The defaults match
// the row as it shipped before columns were configurable; `labels` is opt-in
// because most deployments send no request labels.
// Pure logic with relative imports so node --test can load it directly.

import * as m from "../../lib/paraglide/messages.js";

export const REQUEST_COLUMNS = [
  { key: "status", label: () => m.requests_column_status(), defaultVisible: true },
  { key: "model", label: () => m.requests_column_model(), defaultVisible: true },
  { key: "path", label: () => m.requests_column_path(), defaultVisible: true },
  { key: "labels", label: () => m.requests_column_labels(), defaultVisible: false },
  { key: "attempts", label: () => m.requests_column_attempts(), defaultVisible: true },
  { key: "time", label: () => m.requests_column_time(), defaultVisible: true },
  { key: "duration", label: () => m.requests_column_duration(), defaultVisible: true },
  { key: "tokens", label: () => m.requests_column_tokens_per_second(), defaultVisible: true },
];

// normalizeRequestColumns turns a stored value ({ [key]: boolean }) into a full
// visibility map. Unknown keys are dropped and missing ones fall back to the
// default, so a column added later shows up with its own default.
export function normalizeRequestColumns(stored) {
  const source = stored && typeof stored === "object" && !Array.isArray(stored) ? stored : {};
  return Object.fromEntries(
    REQUEST_COLUMNS.map((column) => [
      column.key,
      typeof source[column.key] === "boolean" ? source[column.key] : column.defaultVisible,
    ]),
  );
}

// parseStoredRequestColumns reads the localStorage string; bad JSON yields the
// defaults.
export function parseStoredRequestColumns(raw) {
  try {
    return normalizeRequestColumns(JSON.parse(raw));
  } catch {
    return normalizeRequestColumns(null);
  }
}

// requestEntryLabels returns the request labels of a list entry.
export function requestEntryLabels(entry) {
  const labels = entry && entry.data && entry.data.labels;
  return Array.isArray(labels) ? labels : [];
}
