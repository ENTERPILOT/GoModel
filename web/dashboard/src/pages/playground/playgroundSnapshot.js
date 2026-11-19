// Pure logic behind "Open in Playground" — no Svelte runtime, no $lib imports —
// so tests/playground-snapshot.test.js can load it straight into node --test.
//
// A request log entry stores the client's request body verbatim (when body
// logging is on). playgroundSnapshot turns it into what the Playground can
// edit: endpoint, model, stream flag and a role/content text conversation.
// Everything else the request carried — tools, tool calls and results, images,
// sampling parameters, server-side conversation state — has no control in the
// Playground and is left out; `partial` reports that something was.

import { followUpEndpointKind } from "../requests/conversation-helpers.js";
import { addDaysToDateKey, dateToDateKey } from "../../lib/utils/dateKeys.js";

// Request fields the Playground rebuilds itself, per endpoint. Any other
// top-level field marks the snapshot partial.
const CARRIED_FIELDS = {
  chat: new Set(["model", "messages", "stream"]),
  responses: new Set(["model", "input", "instructions", "stream"]),
  messages: new Set(["model", "messages", "system", "stream"]),
};

const TEXT_PART_TYPES = new Set(["text", "input_text", "output_text"]);

// A Map, not an object: a logged role such as "constructor" must not resolve
// to an inherited property.
const SNAPSHOT_ROLES = new Map([
  ["system", "system"],
  ["developer", "system"],
  ["user", "user"],
  ["assistant", "assistant"],
]);

function plainObject(value) {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

// Collects the conversation and remembers whether anything was dropped.
function createCollector() {
  const state = { messages: [], partial: false };

  // content: a string, or an array of parts of which only the text ones are
  // kept. A message left with no text, or with a role the Playground cannot
  // send (tool, function), is dropped whole.
  function add(role, content) {
    const mapped = SNAPSHOT_ROLES.get(role);
    let text = "";
    if (typeof content === "string") {
      text = content;
    } else if (Array.isArray(content)) {
      const texts = [];
      for (const part of content) {
        if (plainObject(part) && TEXT_PART_TYPES.has(part.type) && typeof part.text === "string") {
          texts.push(part.text);
        } else {
          state.partial = true;
        }
      }
      text = texts.join("\n\n");
    }
    if (!mapped || text.trim() === "") {
      state.partial = true;
      return;
    }
    state.messages.push({ role: mapped, content: text });
  }

  // A chat or Anthropic message: role + content, anything else (tool_calls,
  // name, cache_control, ...) is not carried.
  function addMessage(message) {
    if (!plainObject(message)) {
      state.partial = true;
      return;
    }
    if (Object.keys(message).some((key) => key !== "role" && key !== "content")) {
      state.partial = true;
    }
    add(message.role, message.content);
  }

  return { state, add, addMessage };
}

function collectResponsesInput(collector, input) {
  if (typeof input === "string") {
    collector.add("user", input);
    return;
  }
  if (!Array.isArray(input)) {
    if (input != null) collector.state.partial = true;
    return;
  }
  for (const item of input) {
    // Message items may omit "type"; function calls, their outputs, reasoning
    // and item references are not conversation text.
    const isMessage = plainObject(item) &&
      (item.type === "message" || (item.type === undefined && item.role !== undefined));
    if (!isMessage) {
      collector.state.partial = true;
      continue;
    }
    if (Object.keys(item).some((key) => key !== "type" && key !== "role" && key !== "content")) {
      collector.state.partial = true;
    }
    collector.add(item.role, item.content);
  }
}

// playgroundSnapshot maps a full request log entry to
// { endpoint, model, stream, messages: [{ role, content }], partial }, or null
// when the entry has no stored request body the Playground can replay: an
// endpoint it does not speak, body logging off, a body too big to capture, or
// no text message in it.
export function playgroundSnapshot(entry) {
  const endpoint = followUpEndpointKind(entry && entry.path);
  const body = entry && entry.data ? entry.data.request_body : null;
  if (!endpoint || !plainObject(body)) return null;

  const collector = createCollector();
  const carried = CARRIED_FIELDS[endpoint];
  if (Object.keys(body).some((key) => !carried.has(key))) collector.state.partial = true;

  if (endpoint === "responses") {
    if (typeof body.instructions === "string") collector.add("system", body.instructions);
    collectResponsesInput(collector, body.input);
  } else {
    // Anthropic keeps the system prompt outside messages, as a string or as
    // text blocks.
    if (endpoint === "messages" && body.system != null) collector.add("system", body.system);
    if (!Array.isArray(body.messages)) return null;
    body.messages.forEach((message) => collector.addMessage(message));
  }

  const { messages, partial } = collector.state;
  if (messages.length === 0) return null;
  const model = typeof body.model === "string" && body.model.trim()
    ? body.model.trim()
    : String(entry.requested_model || "").trim();
  return { endpoint, model, stream: body.stream === true, messages, partial };
}

// canOpenInPlayground gates the button on a request log row. A full entry is
// judged by its body. A list entry is slim — the server stripped its bodies
// and left `bodies_omitted` plus `conversation_payload` (bodies were stored
// and conversation-shaped) — so it qualifies on those flags and the click
// fetches the detail.
export function canOpenInPlayground(entry) {
  if (!entry || !followUpEndpointKind(entry.path)) return false;
  const data = entry.data || {};
  if (data.request_body_too_big_to_handle) return false;
  if (data.request_body != null) return playgroundSnapshot(entry) !== null;
  return Boolean(entry.id && entry.bodies_omitted && entry.conversation_payload);
}

// A usage log row carries no bodies, only the request it belongs to; it can be
// opened when it has a request id on an endpoint the Playground speaks. Whether
// the request log still holds its body is only known after the lookup.
export function canOpenUsageEntryInPlayground(entry) {
  return Boolean(
    entry && String(entry.request_id || "").trim() && followUpEndpointKind(entry.endpoint),
  );
}

// How an admin fetch made for the button ended: "ok", "missing" (404),
// "failed", or "silent" when the API client already dealt with it — a response
// for a previous API key, or a 401 / dashboard-access 403 that opened the auth
// dialog with its own explanation.
export function requestLogFetchOutcome(result) {
  if (!result) return "failed";
  if (result.stale || result.status === 401) return "silent";
  if (result.status === 403 && result.data?.error?.code === "dashboard_access_denied") {
    return "silent";
  }
  if (result.status === 404) return "missing";
  return result.ok ? "ok" : "failed";
}

// Request ids are not unique: the gateway keeps a caller-supplied
// X-Request-Id, so retries or unrelated calls can share one. The lookup
// therefore fetches several matches and usageRequestLogMatch picks the one the
// usage row belongs to.
const USAGE_LOOKUP_LIMIT = 20;
// Usage is recorded when the response ends, on the clock that timed the
// request; the slack covers rounding and the write itself.
const USAGE_MATCH_SLACK_MS = 2000;

// usageRequestLogMatch picks the request log entry of a usage row from the
// entries sharing its request id: { entry, ambiguous }. A single entry on the
// row's endpoint is the match. Several are narrowed to the one whose duration
// spans the moment the usage was recorded; when that does not single one out —
// or the page did not hold every match — the result is ambiguous and nothing
// is opened, rather than some other request's conversation.
export function usageRequestLogMatch(usageEntry, entries, total) {
  const requestID = String(usageEntry?.request_id || "").trim();
  const endpoint = followUpEndpointKind(usageEntry?.endpoint);
  const list = Array.isArray(entries) ? entries : [];
  const candidates = list.filter((entry) =>
    entry && entry.id &&
    String(entry.request_id || "").trim() === requestID &&
    followUpEndpointKind(entry.path) === endpoint);
  const complete = !(Number(total) > list.length);
  if (complete && candidates.length <= 1) {
    return { entry: candidates[0] || null, ambiguous: false };
  }
  const recordedAt = Date.parse(usageEntry?.timestamp);
  const spanning = candidates.filter((entry) => {
    const started = Date.parse(entry.timestamp);
    const ended = started + Number(entry.duration_ns || 0) / 1e6;
    return recordedAt >= started - USAGE_MATCH_SLACK_MS && recordedAt <= ended + USAGE_MATCH_SLACK_MS;
  });
  if (complete && spanning.length === 1) return { entry: spanning[0], ambiguous: false };
  return { entry: null, ambiguous: true };
}

// Query for GET /admin/request-logs that finds the request log entries of a
// usage row. The list defaults to the trailing 30 days, so the row's own day
// is passed explicitly, widened by a day each side to cover every dashboard
// timezone.
export function usageEntryRequestLogQuery(entry) {
  const params = new URLSearchParams();
  params.set("request_id", String(entry?.request_id || "").trim());
  params.set("limit", String(USAGE_LOOKUP_LIMIT));
  const day = dateToDateKey(new Date(entry?.timestamp));
  if (day) {
    params.set("start_date", addDaysToDateKey(day, -1));
    params.set("end_date", addDaysToDateKey(day, 1));
  }
  return params.toString();
}
