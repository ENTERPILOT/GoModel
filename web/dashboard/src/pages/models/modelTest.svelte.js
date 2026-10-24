// Per-row model test results, keyed by the display row key so a result
// survives inventory refetches that rebuild the rows. Each entry is
// { state: "running" } or the summary from summarizeModelTest. Requests use
// apiFetch like the Playground: a provider rejecting its own key is an
// ordinary test failure, only a gateway 401 reopens the auth dialog.

import { apiFetch, isAbortError } from "$lib/api/client.js";
import { isGatewayAuthError } from "$lib/api/errors.js";
import { auth } from "$lib/stores/auth.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { playgroundUserPathHeader } from "$pages/playground/playgroundLogic.js";
import {
  MODEL_TEST_PATH,
  MODEL_TEST_TIMEOUT_MS,
  buildModelTestRequest,
  modelTestSelector,
  modelTestUserPath,
  summarizeModelTest,
} from "./modelTest.js";

class ModelTestState {
  #results = $state({});
  // auth.generation the results belong to; a key change hides them and
  // drops replies to requests sent with the earlier key.
  #generation = auth.generation;

  #current() {
    return this.#generation === auth.generation ? this.#results : {};
  }

  result(row) {
    return row && row.key ? this.#current()[row.key] || null : null;
  }

  isRunning(row) {
    return this.result(row)?.state === "running";
  }

  dismiss(row) {
    if (!row || !row.key || this.isRunning(row)) return;
    const next = { ...this.#current() };
    delete next[row.key];
    this.#generation = auth.generation;
    this.#results = next;
  }

  #set(key, value, generation) {
    if (generation !== auth.generation) return;
    const current = this.#current();
    this.#generation = generation;
    this.#results = { ...current, [key]: value };
  }

  async run(row) {
    const selector = modelTestSelector(row);
    if (!selector || this.isRunning(row)) return;
    const key = row.key;
    const userPath = modelTestUserPath(row);
    const generation = auth.generation;
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), MODEL_TEST_TIMEOUT_MS);
    const started = performance.now();
    const finish = (outcome, fallback = m.playground_request_failed()) =>
      this.#set(
        key,
        summarizeModelTest({ ...outcome, durationMs: performance.now() - started }, fallback),
        generation,
      );
    this.#set(key, { state: "running" }, generation);

    try {
      // A restricted model needs its user path under the configured
      // USER_PATH_HEADER, so never guess the header name.
      if (userPath) {
        await runtimeConfig.ensureLoaded();
        if (!runtimeConfig.loaded) {
          finish({ ok: false }, m.playground_config_unavailable());
          return;
        }
      }
      const res = await apiFetch(MODEL_TEST_PATH, {
        method: "POST",
        body: JSON.stringify(buildModelTestRequest(selector)),
        headers: playgroundUserPathHeader(userPath, runtimeConfig.userPathHeader()),
        signal: controller.signal,
      });
      let data = null;
      try {
        data = await res.json();
      } catch {
        data = null;
      }
      if (res.status === 401 && isGatewayAuthError(data)) {
        auth.handleUnauthorized(generation);
      }
      finish({ ok: res.ok, status: res.status, data });
    } catch (error) {
      if (!isAbortError(error)) console.error("Model test failed:", error);
      finish(
        { ok: false },
        controller.signal.aborted
          ? m.models_test_timed_out({ seconds: MODEL_TEST_TIMEOUT_MS / 1000 })
          : m.playground_request_failed(),
      );
    } finally {
      clearTimeout(timeout);
    }
  }
}

export const modelTestState = new ModelTestState();
