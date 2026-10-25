// Request types and endpoints on the usage page: endpoint filter choices and
// the by-type / by-endpoint breakdown rows.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import * as m from "../../lib/paraglide/messages.js";
import { AUDIT_TYPES } from "../audit-logs/audit-operations.js";

// The usage page groups endpoints by the same request types as the audit
// log: each type covers one or more gateway operations, so
// /v1/chat/completions and /v1/messages both count as chat. Endpoints
// outside every type fall under "other". The page filters by endpoint, so a
// selection can mix endpoints of different types.

export const USAGE_TYPE_OTHER = "other";

const TYPE_BY_OPERATION = new Map(
  AUDIT_TYPES.flatMap((type) => type.operations.map((op) => [op, type])),
);
const TYPE_BY_KEY = new Map(AUDIT_TYPES.map((type) => [type.key, type]));
const TYPE_ORDER = new Map(AUDIT_TYPES.map((type, index) => [type.key, index]));

// usageTypeForOperation returns the type key an endpoint's operation belongs to.
export function usageTypeForOperation(operation) {
  const type = TYPE_BY_OPERATION.get(String(operation || ""));
  return type ? type.key : USAGE_TYPE_OTHER;
}

export function usageTypeLabel(key) {
  const type = TYPE_BY_KEY.get(key);
  return type ? type.label() : m.usage_type_other();
}

function typeOrder(key) {
  return TYPE_ORDER.has(key) ? TYPE_ORDER.get(key) : AUDIT_TYPES.length;
}

// usageEndpointOptions builds the endpoint filter choices from
// /admin/usage/endpoints rows: grouped in the audit log's type order, each
// described by its type.
export function usageEndpointOptions(rows) {
  const byEndpoint = new Map();
  for (const row of rows || []) {
    if (!row || !row.endpoint || byEndpoint.has(row.endpoint)) continue;
    byEndpoint.set(row.endpoint, usageTypeForOperation(row.operation));
  }
  return [...byEndpoint.entries()]
    .sort(([a, typeA], [b, typeB]) => typeOrder(typeA) - typeOrder(typeB) || a.localeCompare(b))
    .map(([endpoint, type]) => ({ value: endpoint, label: endpoint, description: usageTypeLabel(type) }));
}

// endpointOptionsWithSelection keeps every selected endpoint listed, even one
// without traffic under the other filters, so the dropdown can still uncheck
// it — the endpoint counterpart of facetOptionList.
export function endpointOptionsWithSelection(options, selected) {
  const list = Array.isArray(options) ? options : [];
  const listed = new Set(list.map((option) => option.value));
  const missing = (selected || [])
    .filter((endpoint) => endpoint && !listed.has(endpoint))
    .map((endpoint) => ({ value: endpoint, label: endpoint }));
  return list.concat(missing);
}

// toggleEndpointSelection adds the endpoints to the selection, or removes
// them when every one is already selected — one chip click toggles a whole
// request type.
export function toggleEndpointSelection(selected, endpoints) {
  const current = Array.isArray(selected) ? selected : [];
  const targets = (endpoints || []).filter(Boolean);
  if (targets.length === 0) return current.slice();
  if (targets.every((endpoint) => current.includes(endpoint))) {
    return current.filter((endpoint) => !targets.includes(endpoint));
  }
  return current.concat(targets.filter((endpoint) => !current.includes(endpoint)));
}

export function endpointsSelected(selected, endpoints) {
  const targets = (endpoints || []).filter(Boolean);
  return targets.length > 0 && targets.every((endpoint) => (selected || []).includes(endpoint));
}

// endpointUsageRows tags /admin/usage/endpoints rows for the by-endpoint
// view: each keeps its own totals and gains its request type.
export function endpointUsageRows(rows) {
  return (rows || []).filter(Boolean).map((row) => {
    const type = usageTypeForOperation(row.operation);
    return { ...row, type, type_label: usageTypeLabel(type), label: row.endpoint || "-" };
  });
}

// Nullable sums follow SQL SUM: null only when every value is null.
function addNullable(total, value) {
  if (value === null || value === undefined) return total;
  return (total ?? 0) + Number(value);
}

const TYPE_SUM_FIELDS = [
  "requests",
  "input_tokens",
  "output_tokens",
  "total_tokens",
  "uncached_input_tokens",
  "cached_input_tokens",
  "cache_write_input_tokens",
  "local_cached_input_tokens",
  "local_cached_output_tokens",
];
const TYPE_NULLABLE_FIELDS = ["input_cost", "output_cost", "total_cost", "cached_input_cost"];

// foldEndpointUsageByType sums /admin/usage/endpoints rows into one row per
// request type, keeping the folded endpoints sorted.
export function foldEndpointUsageByType(rows) {
  const byType = new Map();
  for (const row of rows || []) {
    if (!row) continue;
    const key = usageTypeForOperation(row.operation);
    let folded = byType.get(key);
    if (!folded) {
      folded = { type: key, label: usageTypeLabel(key), endpoints: [] };
      for (const field of TYPE_SUM_FIELDS) folded[field] = 0;
      for (const field of TYPE_NULLABLE_FIELDS) folded[field] = null;
      byType.set(key, folded);
    }
    if (row.endpoint) folded.endpoints.push(row.endpoint);
    for (const field of TYPE_SUM_FIELDS) folded[field] += Number(row[field]) || 0;
    for (const field of TYPE_NULLABLE_FIELDS) folded[field] = addNullable(folded[field], row[field]);
  }
  for (const folded of byType.values()) folded.endpoints.sort();
  return [...byType.values()];
}

// endpointListSummary shows the first few folded endpoints; the table cell's
// tooltip carries the full list.
export function endpointListSummary(endpoints, max = 3) {
  const list = endpoints || [];
  if (list.length === 0) return "-";
  const shown = list.slice(0, max).join(", ");
  return list.length > max ? shown + " +" + (list.length - max) : shown;
}
