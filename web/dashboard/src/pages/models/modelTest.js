// Per-row "test this model" logic: which rows get the button, the request it
// sends, and how the reply is summarized. Pure (no Svelte, relative imports)
// so tests/models-test.test.js can load it straight into node --test.
//
// The test goes through the same public endpoint and request builder as the
// Playground (POST /v1/chat/completions with the dashboard key), so it shows
// up in Audit Logs and Usage like any other client's request.

import { errorPayloadMessage } from "../../lib/api/errors.js";
import { buildPlaygroundRequest, extractResponseText } from "../playground/playgroundLogic.js";

export const MODEL_TEST_PATH = "/v1/chat/completions";
export const MODEL_TEST_PROMPT = "Say hello in one short sentence.";
export const MODEL_TEST_MAX_TOKENS = 64;
// A test that has not answered by then is reported as timed out.
export const MODEL_TEST_TIMEOUT_MS = 60_000;
// Longest reply excerpt shown inline in the row.
export const MODEL_TEST_SNIPPET_LENGTH = 120;

// Modes advertised in model metadata that a chat request can exercise.
const CHAT_MODES = new Set(["chat", "completion", "responses", "messages"]);

// The selector the row stands for: the virtual model name for alias rows,
// the provider-qualified selector for concrete models.
export function modelTestSelector(row) {
  if (!row) return "";
  if (row.is_alias) return String(row.alias?.name || row.display_name || "").trim();
  return String(row.selector || row.display_name || "").trim();
}

// rowCanTestModel offers the test only where a chat prompt makes sense:
// enabled rows whose metadata says text generation. Rows without category or
// mode metadata (local models, virtual models over unknown targets) get the
// button too — the reply tells the user whether chat works.
export function rowCanTestModel(row) {
  if (!row || !modelTestSelector(row)) return false;
  if (row.is_alias && row.alias?.enabled === false) return false;
  if (!row.is_alias && row.access?.effective_enabled === false) return false;
  const metadata = row.model?.metadata;
  const categories = Array.isArray(metadata?.categories) ? metadata.categories : [];
  if (categories.length > 0) return categories.includes("text_generation");
  const modes = Array.isArray(metadata?.modes) ? metadata.modes : [];
  if (modes.length > 0) return modes.some((mode) => CHAT_MODES.has(mode));
  return true;
}

// First user path a restricted model allows, sent on the user-path header so
// the test passes the same access check the Playground's would.
export function modelTestUserPath(row) {
  const paths = Array.isArray(row?.access?.user_paths) ? row.access.user_paths : [];
  const path = paths.find((value) => typeof value === "string" && value.trim() !== "");
  return path ? path.trim() : "";
}

export function buildModelTestRequest(selector) {
  return buildPlaygroundRequest("chat", {
    model: selector,
    messages: [{ role: "user", content: MODEL_TEST_PROMPT }],
    maxTokens: MODEL_TEST_MAX_TOKENS,
  });
}

// Collapses whitespace and trims the reply to a one-line excerpt.
export function replySnippet(text, maxLength = MODEL_TEST_SNIPPET_LENGTH) {
  const flat = String(text ?? "").replace(/\s+/g, " ").trim();
  if (flat.length <= maxLength) return flat;
  return flat.slice(0, Math.max(0, maxLength - 1)).trimEnd() + "…";
}

// summarizeModelTest turns one finished request into the row's result:
//   { state: "ok", durationMs, reply, finishReason }
//   { state: "error", durationMs, status, message }
// A 2xx answer counts as success even with no text (a reasoning model can
// spend the small token budget thinking); finishReason says why it is empty.
export function summarizeModelTest({ ok, status, data, durationMs }, fallbackMessage = "") {
  const duration = Math.max(0, Math.round(Number(durationMs) || 0));
  if (!ok) {
    return {
      state: "error",
      durationMs: duration,
      status: Number(status) || 0,
      message: errorPayloadMessage(data, fallbackMessage),
    };
  }
  const choice = Array.isArray(data?.choices) ? data.choices[0] : null;
  return {
    state: "ok",
    durationMs: duration,
    reply: replySnippet(extractResponseText("chat", data)),
    finishReason: String(choice?.finish_reason || ""),
  };
}
