// Canonical audit-record merging shared by live events, list/detail fetches,
// and the Interactions drawer. Sources may omit expensive fields, so merging
// is deliberately monotonic: a slim or replayed record cannot erase richer
// data that the browser already received.

const lifecycleRank = {
  "audit.started": 10,
  "audit.updated": 20,
  "audit.stream": 20,
  "audit.completed": 30,
  "audit.failed": 40,
  "audit.flushed": 40,
  "audit.detail": 40,
};
const liveAuditEvents = new Set(Object.keys(lifecycleRank));

export function auditRecordKey(entry) {
  return String(entry && (entry.id || entry.request_id) || "").trim();
}

export function mergeAuditRecord(previous, incoming) {
  const current = previous && typeof previous === "object" ? previous : {};
  const patch = incoming && typeof incoming === "object" ? incoming : {};
  const merged = { ...current, ...patch };

  if (patch.data == null && current.data != null) {
    merged.data = current.data;
  } else if (plainObject(current.data) && plainObject(patch.data)) {
    merged.data = { ...current.data, ...patch.data };
    const revisions = mergedRequestRevisions(
      current.data.request_revisions,
      patch.data.request_revisions,
    );
    if (revisions) merged.data.request_revisions = revisions;
  }

  // Lifecycle fields belong to the live transport. Persisted/list projections
  // normally omit them; late or replayed events must not move them backwards.
  const previousState = String(current._live_state || "").trim();
  const incomingState = String(patch._live_state || "").trim();
  if ((lifecycleRank[previousState] || 0) > (lifecycleRank[incomingState] || 0)) {
    merged._live_state = previousState;
  }
  if (current._audit_flushed) merged._audit_flushed = true;
  if (merged._audit_flushed) merged._live_pending = false;

  // Full detail is sticky. A later slim list projection must not make callers
  // fetch the same detail again or advertise that its bodies are missing.
  if (current._detail_loaded && !patch._detail_loaded) {
    merged._detail_loaded = true;
    merged.bodies_omitted = false;
  }
  if (merged.bodies_omitted && plainObject(merged.data) &&
      (merged.data.request_body !== undefined || merged.data.response_body !== undefined)) {
    merged.bodies_omitted = false;
  }

  return merged;
}

export function auditRecordChangesAfter(changes, version) {
  const seen = Number(version || 0);
  return (Array.isArray(changes) ? changes : []).filter((change) =>
    Number(change && change.version || 0) > seen);
}

export function isLiveAuditRecordChange(change) {
  return liveAuditEvents.has(String(change && change.eventType || "").trim());
}

// Revision bodies arrive only via the detail endpoint; slim projections
// (conversation, list) carry metadata-only revisions. Merge per step so a
// later slim refresh cannot downgrade an already-loaded rewrite body.
function mergedRequestRevisions(currentRevisions, patchRevisions) {
  if (!Array.isArray(currentRevisions) || !Array.isArray(patchRevisions)) {
    return null;
  }
  return patchRevisions.map((revision) => {
    const seq = Number(revision && revision.seq || 0);
    const previous = currentRevisions.find(
      (candidate) => Number(candidate && candidate.seq || 0) === seq,
    );
    if (!previous) return revision;
    const richer = { ...revision };
    if ((richer.body == null || richer.body === "") && previous.body != null) {
      richer.body = previous.body;
    }
    if (richer.detail == null && previous.detail != null) {
      richer.detail = previous.detail;
    }
    return richer;
  });
}

function plainObject(value) {
  return !!value && typeof value === "object" && !Array.isArray(value);
}
