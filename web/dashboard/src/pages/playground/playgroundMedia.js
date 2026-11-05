// Pure logic for the playground's media modes — no Svelte runtime, no $lib
// imports — so tests/playground-media.test.js can load it into node --test.
//
// Besides the chat conversation, the playground can exercise the gateway's
// OpenAI-compatible media endpoints: image generation, text-to-speech and
// speech-to-text. Each mode has one request builder and, where the response
// is JSON, one reader for what the UI renders.

import { playgroundModelOptions } from "./playgroundLogic.js";

export const DEFAULT_MODE = "chat";

// `modelModes` are the /admin/models metadata modes that qualify a model for
// the mode's picker (see core.modeToCategory).
export const MEDIA_MODES = [
  { id: "image", path: "/v1/images/generations", modelModes: ["image_generation"] },
  { id: "speech", path: "/v1/audio/speech", modelModes: ["audio_speech"] },
  { id: "transcription", path: "/v1/audio/transcriptions", modelModes: ["audio_transcription"] },
];

export const PLAYGROUND_MODES = [DEFAULT_MODE, ...MEDIA_MODES.map((mode) => mode.id)];

// Sizes OpenAI's image models accept; "" leaves the size to the provider.
export const IMAGE_SIZES = ["", "256x256", "512x512", "1024x1024", "1024x1536", "1536x1024", "1792x1024", "1024x1792"];
export const MAX_IMAGE_COUNT = 4;
export const SPEECH_VOICES = ["alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse"];
export const DEFAULT_SPEECH_VOICE = "alloy";
// Formats an <audio> element can play; pcm is headerless and is left out.
export const SPEECH_FORMATS = ["mp3", "wav", "opus", "aac", "flac"];
export const DEFAULT_SPEECH_FORMAT = "mp3";

export function mediaModeById(id) {
  return MEDIA_MODES.find((mode) => mode.id === id) || null;
}

export function normalizeMode(id) {
  return PLAYGROUND_MODES.includes(id) ? id : DEFAULT_MODE;
}

// Model picker options for a mode. Chat keeps the text-model list; a media
// mode lists the models advertising it first, then those without mode
// metadata (they might support it), so the default pick is a sure one.
export function modeModelOptions(inventory, modeID) {
  const mode = mediaModeById(modeID);
  if (!mode) return playgroundModelOptions(inventory);
  const wanted = new Set(mode.modelModes);
  const advertised = new Set(
    (Array.isArray(inventory) ? inventory : [])
      .filter((entry) => (entry?.model?.metadata?.modes || []).some((name) => wanted.has(name)))
      .map((entry) => String(entry?.selector || entry?.model?.id || "").trim()),
  );
  const options = playgroundModelOptions(inventory, wanted);
  return [
    ...options.filter((option) => advertised.has(option.id)),
    ...options.filter((option) => !advertised.has(option.id)),
  ];
}

function text(value) {
  return String(value ?? "").trim();
}

function imageCount(value) {
  const n = Number(value);
  return Number.isInteger(n) && n > 1 ? Math.min(n, MAX_IMAGE_COUNT) : 1;
}

// Body for POST /v1/images/generations. n and size are sent only when they
// differ from the provider defaults, so the preview stays minimal.
export function buildImageRequest({ model, prompt, size, n } = {}) {
  const body = { model: text(model), prompt: String(prompt ?? "") };
  const count = imageCount(n);
  if (count > 1) body.n = count;
  if (IMAGE_SIZES.includes(size) && size) body.size = size;
  return body;
}

// Body for POST /v1/audio/speech. mp3 is the API default, so response_format
// is sent only for another format.
export function buildSpeechRequest({ model, input, voice, format } = {}) {
  const body = {
    model: text(model),
    input: String(input ?? ""),
    voice: text(voice) || DEFAULT_SPEECH_VOICE,
  };
  if (SPEECH_FORMATS.includes(format) && format !== DEFAULT_SPEECH_FORMAT) {
    body.response_format = format;
  }
  return body;
}

// Multipart fields for POST /v1/audio/transcriptions, in send order, without
// the file part. Blank optional fields are dropped.
export function transcriptionFields({ model, language, prompt } = {}) {
  const fields = [["model", text(model)]];
  if (text(language)) fields.push(["language", text(language)]);
  if (text(prompt)) fields.push(["prompt", text(prompt)]);
  return fields;
}

// What the JSON panel shows for a multipart transcription request: the form
// fields plus a description of the file part.
export function transcriptionPreview(fields, file) {
  const preview = Object.fromEntries(fields);
  preview.file = file
    ? { name: String(file.name || ""), type: String(file.type || ""), size: Number(file.size || 0) }
    : null;
  return preview;
}

const IMAGE_MIME = { png: "image/png", jpeg: "image/jpeg", jpg: "image/jpeg", webp: "image/webp" };

// Images in an /v1/images/generations response as [{src, revisedPrompt}]:
// hosted URLs as-is, inline base64 as data URLs typed by output_format.
export function imageResults(body) {
  const data = body && Array.isArray(body.data) ? body.data : [];
  const mime = IMAGE_MIME[String(body?.output_format || "").toLowerCase()] || "image/png";
  return data
    .map((item) => {
      if (!item || typeof item !== "object") return null;
      const src = item.b64_json
        ? "data:" + mime + ";base64," + item.b64_json
        : String(item.url || "");
      return src ? { src, revisedPrompt: String(item.revised_prompt || "") } : null;
    })
    .filter(Boolean);
}

// Inline base64 images are megabytes of text; the JSON panel shows a stub in
// their place so it stays readable. Everything else is passed through.
export function abbreviateImageResponse(body) {
  if (!body || typeof body !== "object" || !Array.isArray(body.data)) return body;
  return {
    ...body,
    data: body.data.map((item) =>
      item && typeof item.b64_json === "string" && item.b64_json.length > 64
        ? { ...item, b64_json: item.b64_json.slice(0, 32) + "… (" + item.b64_json.length + " chars)" }
        : item,
    ),
  };
}

// Text of a transcription response: the JSON formats carry it in `text`;
// text, srt and vtt formats are the body itself.
export function transcriptionText(body) {
  if (typeof body === "string") return body;
  return body && typeof body === "object" ? String(body.text ?? "") : "";
}
