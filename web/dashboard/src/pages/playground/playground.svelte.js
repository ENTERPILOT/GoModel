// Playground state: the editable conversation, the endpoint/model selection
// and the in-flight request. Requests go straight to the gateway's public API
// (/v1/chat/completions, /v1/responses, /v1/messages) with the dashboard key,
// so they show up in Audit Logs and Usage like any other client's traffic.

import { apiFetch, isAbortError } from "$lib/api/client.js";
import { errorPayloadMessage, isGatewayAuthError } from "$lib/api/errors.js";
import { consumeEventStream } from "$lib/api/eventStream.js";
import { auth } from "$lib/stores/auth.svelte.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import { readStored, writeStored } from "$lib/utils/storage.js";
import { moveItem } from "$lib/utils/sortable.js";
import * as m from "$lib/paraglide/messages.js";
import {
  buildPlaygroundRequest,
  createStreamAccumulator,
  defaultUserPathForModel,
  endpointById,
  extractResponseText,
  extractUsage,
  initialJsonPanelOpen,
  normalizeEndpoint,
  normalizeRole,
  playgroundUserPathHeader,
} from "./playgroundLogic.js";

const STORAGE = {
  endpoint: "gomodel_playground_endpoint",
  model: "gomodel_playground_model",
  stream: "gomodel_playground_stream",
  panel: "gomodel_playground_json_panel",
};

class PlaygroundStore {
  endpoint = $state(normalizeEndpoint(readStored(STORAGE.endpoint, "")));
  model = $state(readStored(STORAGE.model, "") || "");
  // Session-only: the user path to send as X-GoModel-User-Path. Defaults to
  // the selected model's first allowed path when setModel picks a
  // restricted model; never persisted to localStorage.
  userPath = $state("");
  stream = $state(readStored(STORAGE.stream, "true") !== "false");
  // [{ id, role, content, pending }] — the conversation the user edits.
  messages = $state([]);
  draft = $state("");
  sending = $state(false);
  // Whether the in-flight request asked for a stream (the toggle may change
  // while it runs).
  sendingStream = $state(false);
  error = $state("");
  // Last response body (assembled from events when streaming) and its
  // {status, durationMs, streamed, events, usage} summary.
  response = $state(null);
  responseMeta = $state(null);
  panelOpen = $state(
    initialJsonPanelOpen(
      readStored(STORAGE.panel, "true"),
      typeof window === "undefined" ? Infinity : window.innerWidth,
    ),
  );
  panelTab = $state("request");

  requestBody = $derived(
    buildPlaygroundRequest(this.endpoint, {
      model: this.model,
      messages: this.messages,
      stream: this.stream,
    }),
  );

  #abort = null;
  #nextID = 1;

  get endpointPath() {
    return endpointById(this.endpoint)?.path || "";
  }

  get canSend() {
    return !this.sending && (this.draft.trim() !== "" || this.messages.length > 0);
  }

  setEndpoint(id) {
    this.endpoint = normalizeEndpoint(id);
    writeStored(STORAGE.endpoint, this.endpoint);
  }

  setModel(model) {
    this.model = String(model || "");
    writeStored(STORAGE.model, this.model);
    this.userPath = defaultUserPathForModel(modelsStore.models, this.model.trim());
  }

  setUserPath(value) {
    this.userPath = String(value || "");
  }

  setStream(enabled) {
    this.stream = Boolean(enabled);
    writeStored(STORAGE.stream, this.stream);
  }

  togglePanel() {
    this.panelOpen = !this.panelOpen;
    writeStored(STORAGE.panel, this.panelOpen);
  }

  // Appends a message and returns its reactive proxy so callers can keep
  // mutating it (the streaming assistant bubble does).
  addMessage(role, content = "") {
    this.messages.push({
      id: this.#nextID++,
      role: normalizeRole(role),
      content: String(content),
      pending: false,
    });
    return this.messages[this.messages.length - 1];
  }

  removeMessage(id) {
    this.messages = this.messages.filter((message) => message.id !== id);
  }

  moveMessage(from, to) {
    this.messages = moveItem(this.messages, from, to);
  }

  // Keyboard counterpart of drag-to-reorder: shift one message by `delta`.
  nudgeMessage(id, delta) {
    const from = this.messages.findIndex((message) => message.id === id);
    if (from < 0) return;
    this.moveMessage(from, Math.max(0, Math.min(this.messages.length - 1, from + delta)));
  }

  clear() {
    this.stop();
    this.messages = [];
    this.error = "";
    this.response = null;
    this.responseMeta = null;
  }

  stop() {
    if (this.#abort) this.#abort.abort();
  }

  async send() {
    if (!this.canSend) return;
    const draft = this.draft.trim();
    if (draft) {
      this.addMessage("user", draft);
      this.draft = "";
    }
    if (!String(this.model || "").trim()) {
      this.error = m.playground_model_required();
      return;
    }
    const endpoint = endpointById(this.endpoint);
    const body = this.requestBody;
    if (!endpoint || !body) return;

    const controller = new AbortController();
    this.#abort = controller;
    this.sending = true;
    this.sendingStream = body.stream === true;
    this.error = "";
    this.response = null;
    this.responseMeta = null;
    const generation = auth.generation;
    const started = performance.now();
    const assistant = this.addMessage("assistant", "");
    assistant.pending = true;
    const meta = { status: 0, durationMs: 0, streamed: false, events: 0, usage: null };

    try {
      const options = {
        method: "POST",
        body: JSON.stringify(body),
        headers: playgroundUserPathHeader(this.userPath),
        signal: controller.signal,
      };
      const res = await apiFetch(endpoint.path, options);
      meta.status = res.status;
      if (!res.ok) {
        let payload = null;
        try {
          payload = await res.json();
        } catch {
          payload = null;
        }
        this.response = payload;
        // A provider rejecting its own key is an ordinary request error;
        // only the gateway rejecting the dashboard key reopens the dialog.
        if (res.status === 401 && isGatewayAuthError(payload)) {
          auth.handleUnauthorized(generation);
          this.error = m.common_authentication_required();
          return;
        }
        this.error = errorPayloadMessage(payload, m.playground_request_failed());
        return;
      }
      const streamed = body.stream === true && res.body &&
        String(res.headers.get("Content-Type") || "").includes("text/event-stream");
      if (streamed) {
        meta.streamed = true;
        const accumulator = createStreamAccumulator(endpoint.id);
        await consumeEventStream(res.body.getReader(), (event) => {
          const delta = accumulator.push(event);
          if (delta) assistant.content += delta;
        });
        meta.events = accumulator.events;
        this.response = accumulator.result();
        if (accumulator.error) this.error = accumulator.error;
      } else {
        const data = await res.json();
        this.response = data;
        assistant.content = extractResponseText(endpoint.id, data);
      }
      meta.usage = extractUsage(this.response);
      if (!assistant.content && !this.error) this.error = m.playground_empty_response();
    } catch (error) {
      if (!isAbortError(error) && !controller.signal.aborted) {
        console.error("Playground request failed:", error);
        this.error = m.playground_request_failed();
      }
    } finally {
      meta.durationMs = Math.round(performance.now() - started);
      assistant.pending = false;
      if (!assistant.content) this.removeMessage(assistant.id);
      if (this.#abort === controller) {
        this.#abort = null;
        this.sending = false;
      }
      this.responseMeta = meta;
      if (this.response !== null || this.error) this.panelTab = "response";
    }
  }
}

export const playgroundStore = new PlaygroundStore();
