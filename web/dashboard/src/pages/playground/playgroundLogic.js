// Pure playground logic — no Svelte runtime, no $lib imports — so
// tests/playground.test.js can load it straight into node --test.
//
// The playground edits one role/content conversation and sends it through the
// gateway as whichever public API the user picked. Each endpoint has its own
// request shape, its own way of carrying the answer, and its own SSE event
// vocabulary; the three families below keep those differences in one place:
//
//   buildPlaygroundRequest   conversation  -> request body
//   extractResponseText      response body -> assistant text (non-streaming)
//   createStreamAccumulator  SSE events    -> assistant text + assembled body

export const ENDPOINTS = [
  { id: "chat", path: "/v1/chat/completions" },
  { id: "responses", path: "/v1/responses" },
  { id: "messages", path: "/v1/messages" },
];

export const DEFAULT_ENDPOINT = "chat";
export const ROLES = ["system", "user", "assistant"];
// Anthropic's Messages API requires max_tokens; the other endpoints leave it
// to the provider default unless the user sets one.
export const DEFAULT_MAX_TOKENS = 1024;
// Modes advertised by /admin/models metadata that mean "talks text".
const TEXT_MODES = new Set(["chat", "responses", "messages"]);

export function endpointById(id) {
  return ENDPOINTS.find((endpoint) => endpoint.id === id) || null;
}

export function normalizeEndpoint(id) {
  return endpointById(id) ? id : DEFAULT_ENDPOINT;
}

export function normalizeRole(role) {
  return ROLES.includes(role) ? role : "user";
}

function cloneJSON(value) {
  return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

function textOf(message) {
  return String(message?.content ?? "");
}

// Messages with nothing typed in them are dropped from the request — the
// preview shows exactly what goes over the wire.
export function sendableMessages(messages) {
  return (Array.isArray(messages) ? messages : [])
    .filter((message) => textOf(message).trim() !== "")
    .map((message) => ({ role: normalizeRole(message.role), content: textOf(message) }));
}

function splitSystem(messages) {
  const system = messages.filter((m) => m.role === "system");
  const rest = messages.filter((m) => m.role !== "system");
  return {
    system: system.length ? system.map((m) => m.content).join("\n\n") : "",
    rest,
  };
}

// Body for POST {endpoint.path}. Returns null for an unknown endpoint.
export function buildPlaygroundRequest(endpointID, options = {}) {
  const endpoint = endpointById(endpointID);
  if (!endpoint) return null;
  const model = String(options.model || "").trim();
  const stream = Boolean(options.stream);
  const maxTokens = positiveInteger(options.maxTokens);
  const messages = sendableMessages(options.messages);

  if (endpoint.id === "chat") {
    const body = { model, messages };
    if (maxTokens) body.max_tokens = maxTokens;
    if (stream) body.stream = true;
    return body;
  }

  const { system, rest } = splitSystem(messages);
  if (endpoint.id === "responses") {
    const body = { model };
    if (system) body.instructions = system;
    body.input = rest;
    if (maxTokens) body.max_output_tokens = maxTokens;
    if (stream) body.stream = true;
    return body;
  }

  const body = { model, max_tokens: maxTokens || DEFAULT_MAX_TOKENS };
  if (system) body.system = system;
  body.messages = rest;
  if (stream) body.stream = true;
  return body;
}

function positiveInteger(value) {
  const n = Number(value);
  return Number.isInteger(n) && n > 0 ? n : 0;
}

// --- Non-streaming responses -----------------------------------------------

function partsText(parts, textTypes) {
  if (typeof parts === "string") return parts;
  if (!Array.isArray(parts)) return "";
  return parts
    .filter((part) => part && typeof part === "object" && textTypes.has(part.type))
    .map((part) => String(part.text ?? ""))
    .join("");
}

export function extractResponseText(endpointID, body) {
  if (!body || typeof body !== "object") return "";
  switch (endpointID) {
    case "chat": {
      const message = Array.isArray(body.choices) ? body.choices[0]?.message : null;
      if (!message) return "";
      const content = partsText(message.content, new Set(["text"]));
      return content || String(message.refusal || "");
    }
    case "responses": {
      if (typeof body.output_text === "string" && body.output_text) return body.output_text;
      const output = Array.isArray(body.output) ? body.output : [];
      return output
        .filter((item) => item && item.type === "message")
        .map((item) => partsText(item.content, new Set(["output_text"])))
        .join("");
    }
    case "messages":
      return partsText(body.content, new Set(["text"]));
    default:
      return "";
  }
}

// Token counts in a shape the UI can show regardless of endpoint, or null.
export function extractUsage(body) {
  const usage = body && typeof body === "object" ? body.usage : null;
  if (!usage || typeof usage !== "object") return null;
  const input = usage.prompt_tokens ?? usage.input_tokens;
  const output = usage.completion_tokens ?? usage.output_tokens;
  if (input === undefined && output === undefined) return null;
  return { input: Number(input || 0), output: Number(output || 0) };
}

// Error text carried by a gateway error payload or an in-stream error event.
export function streamErrorMessage(event) {
  const error = event && typeof event === "object" ? event.error : null;
  if (!error) return "";
  if (typeof error === "string") return error;
  return String(error.message || error.type || "");
}

// --- Streaming ---------------------------------------------------------------

// Accumulates the SSE events of one response. push(event) returns the text
// delta to append to the assistant bubble; result() is the response body as
// assembled so far (the shape a non-streaming call would have returned).
export function createStreamAccumulator(endpointID) {
  const state = { events: 0, error: "", result: null };

  const handlers = {
    chat(event) {
      if (!state.result) {
        state.result = {
          id: event.id,
          object: "chat.completion",
          created: event.created,
          model: event.model,
          choices: [
            { index: 0, message: { role: "assistant", content: "" }, finish_reason: null },
          ],
        };
      }
      if (event.model && !state.result.model) state.result.model = event.model;
      if (event.usage) state.result.usage = cloneJSON(event.usage);
      const choice = Array.isArray(event.choices) ? event.choices[0] : null;
      if (!choice) return "";
      const target = state.result.choices[0];
      if (choice.finish_reason) target.finish_reason = choice.finish_reason;
      const delta = choice.delta || {};
      if (delta.role) target.message.role = delta.role;
      const text = typeof delta.content === "string" ? delta.content : "";
      target.message.content += text;
      return text;
    },

    // The terminal response.completed payload does not always carry `output`
    // (the gateway omits it for providers it converts from chat), so the
    // output list is assembled from the item/delta events and kept unless
    // the final response brings its own.
    responses(event) {
      if (!state.result) state.result = { output: [] };
      const output = state.result.output || (state.result.output = []);
      switch (event.type) {
        case "response.output_item.added": {
          const item = cloneJSON(event.item) || {};
          output[event.output_index ?? output.length] = item;
          return "";
        }
        case "response.output_text.delta": {
          const text = String(event.delta ?? "");
          // Converted providers stream deltas without output_index; those
          // belong to the latest assistant message even when a function_call
          // item was added after it.
          let item = event.output_index === undefined || event.output_index === null
            ? output.findLast((candidate) => candidate && candidate.type === "message")
            : output[event.output_index];
          if (!item || item.type !== "message") {
            item = { type: "message", role: "assistant", content: [] };
            output.push(item);
          }
          if (!Array.isArray(item.content)) item.content = [];
          let part = item.content[event.content_index ?? item.content.length - 1];
          if (!part || part.type !== "output_text") {
            part = { type: "output_text", text: "" };
            item.content.push(part);
          }
          part.text = String(part.text || "") + text;
          return text;
        }
        case "response.created":
        case "response.in_progress":
        case "response.completed":
        case "response.incomplete":
        case "response.failed": {
          const response = cloneJSON(event.response);
          if (!response) return "";
          if (!Array.isArray(response.output) || response.output.length === 0) {
            response.output = output;
          }
          state.result = response;
          return "";
        }
        default:
          return "";
      }
    },

    messages(event) {
      switch (event.type) {
        case "message_start":
          state.result = cloneJSON(event.message) || {};
          state.result.content = [];
          return "";
        case "content_block_start":
          if (!state.result) state.result = { content: [] };
          state.result.content[event.index ?? state.result.content.length] =
            cloneJSON(event.content_block) || {};
          return "";
        case "content_block_delta": {
          if (!state.result) state.result = { content: [] };
          const block = state.result.content[event.index ?? 0] || (state.result.content[event.index ?? 0] = {});
          const delta = event.delta || {};
          if (delta.type === "text_delta") {
            block.type = block.type || "text";
            block.text = (block.text || "") + String(delta.text ?? "");
            return String(delta.text ?? "");
          }
          if (delta.type === "input_json_delta") {
            block.partial_json = (block.partial_json || "") + String(delta.partial_json ?? "");
          }
          return "";
        }
        case "message_delta": {
          if (!state.result) state.result = { content: [] };
          Object.assign(state.result, cloneJSON(event.delta) || {});
          if (event.usage) {
            state.result.usage = { ...(state.result.usage || {}), ...cloneJSON(event.usage) };
          }
          return "";
        }
        default:
          return "";
      }
    },
  };

  const handle = handlers[endpointID] || (() => "");

  return {
    get events() {
      return state.events;
    },
    get error() {
      return state.error;
    },
    push(event) {
      if (!event || typeof event !== "object") return "";
      state.events++;
      const error = streamErrorMessage(event);
      if (error) {
        state.error = error;
        return "";
      }
      return handle(event);
    },
    result() {
      return state.result;
    },
  };
}

// --- Model picker ------------------------------------------------------------

// /admin/models inventory -> [{id, label, provider}] of text-capable, enabled
// models, deduplicated by public selector and sorted. `label` drops a
// "provider/" prefix that merely repeats the provider name, so a picker can
// show the provider once, as a tag; `id` stays the full selector to send.
export function playgroundModelOptions(inventory) {
  const seen = new Map();
  for (const entry of Array.isArray(inventory) ? inventory : []) {
    const id = String(entry?.selector || entry?.model?.id || "").trim();
    if (!id || seen.has(id)) continue;
    if (entry?.access && entry.access.effective_enabled === false) continue;
    const modes = entry?.model?.metadata?.modes;
    if (Array.isArray(modes) && modes.length && !modes.some((mode) => TEXT_MODES.has(mode))) {
      continue;
    }
    const provider = String(entry?.provider_name || "");
    const prefix = provider ? provider.toLowerCase() + "/" : "";
    const label = prefix && id.toLowerCase().startsWith(prefix) && id.length > prefix.length
      ? id.slice(prefix.length)
      : id;
    seen.set(id, { id, label, provider });
  }
  return [...seen.values()].sort((a, b) => a.id.localeCompare(b.id));
}

// --- JSON panel sizing -------------------------------------------------------

export const DEFAULT_JSON_PANEL_WIDTH = 420;
export const MIN_JSON_PANEL_WIDTH = 280;

export function clampJsonPanelWidth(width, viewportWidth) {
  const max = Math.max(MIN_JSON_PANEL_WIDTH, Math.min(760, Math.floor(Number(viewportWidth || 0) * 0.6)));
  const value = Number(width);
  if (!Number.isFinite(value)) return Math.min(DEFAULT_JSON_PANEL_WIDTH, max);
  return Math.min(max, Math.max(MIN_JSON_PANEL_WIDTH, Math.round(value)));
}

export function formatJSON(value) {
  if (value === null || value === undefined) return "";
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}
