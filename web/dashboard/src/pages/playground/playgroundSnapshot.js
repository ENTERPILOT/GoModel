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

const SNAPSHOT_ROLES = {
  system: "system",
  developer: "system",
  user: "user",
  assistant: "assistant",
};

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
    const mapped = SNAPSHOT_ROLES[String(role || "")];
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

// Query for GET /admin/request-logs that finds the request log entry of a
// usage row. The list defaults to the trailing 30 days, so the row's own day
// is passed explicitly, widened by a day each side to cover every dashboard
// timezone.
export function usageEntryRequestLogQuery(entry) {
  const params = new URLSearchParams();
  params.set("request_id", String(entry?.request_id || "").trim());
  params.set("limit", "1");
  const day = dateToDateKey(new Date(entry?.timestamp));
  if (day) {
    params.set("start_date", addDaysToDateKey(day, -1));
    params.set("end_date", addDaysToDateKey(day, 1));
  }
  return params.toString();
}
