// Playground media modes: image generation, text-to-speech and transcription
// requests against the gateway's public API (/v1/images/generations,
// /v1/audio/speech, /v1/audio/transcriptions). The model, user path and JSON
// panel state stay in the chat store, so every mode shares the toolbar.

import { apiFetch, isAbortError } from "$lib/api/client.js";
import { errorPayloadMessage, isGatewayAuthError } from "$lib/api/errors.js";
import { auth } from "$lib/stores/auth.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { abortable, extractUsage, playgroundUserPathHeader } from "./playgroundLogic.js";
import {
  DEFAULT_SPEECH_FORMAT,
  DEFAULT_SPEECH_VOICE,
  abbreviateImageResponse,
  buildImageRequest,
  buildSpeechRequest,
  imageResults,
  mediaModeById,
  transcriptionFields,
  transcriptionPreview,
  transcriptionText,
} from "./playgroundMedia.js";
import { playgroundStore as chat } from "./playground.svelte.js";

class PlaygroundMediaStore {
  imagePrompt = $state("");
  imageSize = $state("");
  imageCount = $state(1);
  speechInput = $state("");
  speechVoice = $state(DEFAULT_SPEECH_VOICE);
  speechFormat = $state(DEFAULT_SPEECH_FORMAT);
  transcriptionFile = $state(null);
  transcriptionLanguage = $state("");
  transcriptionPrompt = $state("");

  sending = $state(false);
  error = $state("");
  response = $state(null);
  responseMeta = $state(null);
  // Rendered results of the last successful request.
  images = $state([]);
  audioURL = $state("");
  transcript = $state("");

  #abort = null;

  get endpointPath() {
    return mediaModeById(chat.mode)?.path || "";
  }

  // The body the JSON panel previews; a transcription is multipart, so its
  // preview describes the file part instead of carrying the bytes.
  requestBody = $derived.by(() => {
    switch (chat.mode) {
      case "image":
        return buildImageRequest({
          model: chat.model,
          prompt: this.imagePrompt,
          size: this.imageSize,
          n: this.imageCount,
        });
      case "speech":
        return buildSpeechRequest({
          model: chat.model,
          input: this.speechInput,
          voice: this.speechVoice,
          format: this.speechFormat,
        });
      case "transcription":
        return transcriptionPreview(this.#fields(), this.transcriptionFile);
      default:
        return null;
    }
  });

  get canSend() {
    if (this.sending) return false;
    switch (chat.mode) {
      case "image":
        return this.imagePrompt.trim() !== "";
      case "speech":
        return this.speechInput.trim() !== "";
      case "transcription":
        return Boolean(this.transcriptionFile);
      default:
        return false;
    }
  }

  #fields() {
    return transcriptionFields({
      model: chat.model,
      language: this.transcriptionLanguage,
      prompt: this.transcriptionPrompt,
    });
  }

  #body() {
    if (chat.mode !== "transcription") return JSON.stringify(this.requestBody);
    const form = new FormData();
    for (const [name, value] of this.#fields()) form.append(name, value);
    form.append("file", this.transcriptionFile, this.transcriptionFile.name);
    return form;
  }

  // Drops the rendered results and frees the audio object URL.
  clear() {
    this.stop();
    this.error = "";
    this.response = null;
    this.responseMeta = null;
    this.images = [];
    this.transcript = "";
    this.#setAudioURL("");
  }

  stop() {
    if (this.#abort) this.#abort.abort();
  }

  // Mode switches go through here so a result (or a picked file, whose input
  // re-mounts empty) never outlives the mode it belongs to.
  switchMode(id) {
    this.clear();
    this.transcriptionFile = null;
    chat.setMode(id);
  }

  #setAudioURL(url) {
    if (this.audioURL) URL.revokeObjectURL(this.audioURL);
    this.audioURL = url;
  }

  async send() {
    if (!this.canSend) return;
    const mode = mediaModeById(chat.mode);
    if (!mode) return;
    if (!String(chat.model || "").trim()) {
      this.error = m.playground_model_required();
      return;
    }

    this.clear();
    const controller = new AbortController();
    this.#abort = controller;
    this.sending = true;
    const generation = auth.generation;
    const started = performance.now();
    const meta = { status: 0, durationMs: 0, streamed: false, events: 0, usage: null };

    try {
      // Same rule as chat: never guess the user-path header name.
      if (String(chat.userPath || "").trim()) {
        await abortable(runtimeConfig.ensureLoaded(), controller.signal);
        if (!runtimeConfig.loaded) {
          this.error = m.playground_config_unavailable();
          return;
        }
      }
      const res = await apiFetch(mode.path, {
        method: "POST",
        body: this.#body(),
        headers: playgroundUserPathHeader(chat.userPath, chat.userPathHeaderName),
        signal: controller.signal,
      });
      meta.status = res.status;
      if (!res.ok) {
        const payload = await res.json().catch(() => null);
        this.response = payload;
        if (res.status === 401 && isGatewayAuthError(payload)) {
          auth.handleUnauthorized(generation);
          this.error = m.common_authentication_required();
          return;
        }
        this.error = errorPayloadMessage(payload, m.playground_request_failed());
        return;
      }
      await this.#readResult(mode.id, res);
      meta.usage = extractUsage(this.response);
    } catch (error) {
      if (!isAbortError(error) && !controller.signal.aborted) {
        console.error("Playground media request failed:", error);
        this.error = m.playground_request_failed();
      }
    } finally {
      meta.durationMs = Math.round(performance.now() - started);
      if (this.#abort === controller) {
        this.#abort = null;
        this.sending = false;
      }
      this.responseMeta = meta;
      if (this.response !== null || this.error) chat.panelTab = "response";
    }
  }

  async #readResult(modeID, res) {
    if (modeID === "speech") {
      // Binary audio: the panel gets a summary, the player an object URL.
      const blob = await res.blob();
      this.response = { content_type: blob.type || res.headers.get("Content-Type") || "", bytes: blob.size };
      if (blob.size === 0) {
        this.error = m.playground_media_empty_response();
        return;
      }
      this.#setAudioURL(URL.createObjectURL(blob));
      return;
    }
    const isJSON = String(res.headers.get("Content-Type") || "").includes("json");
    const data = isJSON ? await res.json() : await res.text();
    if (modeID === "image") {
      this.response = abbreviateImageResponse(data);
      this.images = imageResults(data);
      if (this.images.length === 0) this.error = m.playground_media_empty_response();
      return;
    }
    this.response = data;
    this.transcript = transcriptionText(data);
    if (!this.transcript) this.error = m.playground_media_empty_response();
  }
}

export const playgroundMediaStore = new PlaygroundMediaStore();
