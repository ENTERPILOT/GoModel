// Live token throughput engine (only the slice the overview widget needs).
//
// The buckets come from the backend aggregation endpoint
// (GET /admin/usage/throughput) — the database is the single source of truth —
// so history is present on load and survives refreshes/restarts. The live
// usage SSE stream (GET /admin/live/logs?types=usage) is used only as a
// signal to refetch (debounced) when new usage is persisted; a steady timer
// refetches to keep the window scrolling. Framing and reconnect backoff come
// from the shared $lib/api/eventStream.js. stop() closes the SSE stream,
// clears every timer, and frees the bucket buffer when navigating away.

import { getJSON, apiFetch, isAbortError } from "$lib/api/client.js";
import { consumeEventStream, nextReconnect } from "$lib/api/eventStream.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { GRANULARITIES } from "./liveTokensLogic.js";

const USAGE_FLUSH_DEBOUNCE_MS = 900;

class LiveTokensState {
  granularity = $state("minutes");
  buckets = $state([]);
  active = $state(false);

  // Engine internals (timers, stream state) are deliberately non-reactive.
  #scrollTimer = null;
  #debounceTimer = null;
  #reconnectTimer = null;
  #reconnectAttempts = 0;
  #sseController = null;
  #inFlight = false;

  start() {
    this.stop();
    this.active = true;
    this.fetch();
    this.#restartScroll();
    this.#startUsageSignalStream();
  }

  stop() {
    this.active = false;
    if (this.#scrollTimer) {
      clearInterval(this.#scrollTimer);
      this.#scrollTimer = null;
    }
    if (this.#debounceTimer) {
      clearTimeout(this.#debounceTimer);
      this.#debounceTimer = null;
    }
    if (this.#reconnectTimer) {
      clearTimeout(this.#reconnectTimer);
      this.#reconnectTimer = null;
    }
    this.#reconnectAttempts = 0;
    if (this.#sseController) {
      this.#sseController.abort();
      this.#sseController = null;
    }
    this.buckets = [];
  }

  setGranularity(value) {
    if (!GRANULARITIES[value] || value === this.granularity) {
      return;
    }
    this.granularity = value;
    this.buckets = []; // force a re-bucketed chart
    this.#restartScroll();
    this.fetch();
  }

  #restartScroll() {
    if (this.#scrollTimer) {
      clearInterval(this.#scrollTimer);
      this.#scrollTimer = null;
    }
    const cfg = GRANULARITIES[this.granularity] || GRANULARITIES.minutes;
    this.#scrollTimer = setInterval(() => {
      if (!this.active) {
        return;
      }
      this.fetch();
    }, cfg.refreshMs);
  }

  // Called when the SSE stream reports a persisted usage row: the aggregate
  // has changed, so refetch (debounced to coalesce bursts). Other usage
  // events are the not-yet-persisted echoes the periodic scroll already
  // covers.
  noteUsageEvent(eventType) {
    if (!this.active || eventType !== "usage.flushed") {
      return;
    }
    if (this.#debounceTimer) {
      return;
    }
    this.#debounceTimer = setTimeout(() => {
      this.#debounceTimer = null;
      this.fetch();
    }, USAGE_FLUSH_DEBOUNCE_MS);
  }

  async fetch() {
    if (!this.active || this.#inFlight) {
      return;
    }
    this.#inFlight = true;
    const requested = this.granularity;
    try {
      const cfg = GRANULARITIES[requested] || GRANULARITIES.minutes;
      const result = await getJSON(
        "/admin/usage/throughput?granularity=" + cfg.apiName,
        { label: "token throughput" },
      );
      if (result.stale || !result.ok) {
        return;
      }
      // The granularity changed while this request was in flight, so its
      // buckets no longer match the selected window — discard them (a fresh
      // fetch is issued in finally).
      if (this.granularity !== requested) {
        return;
      }
      this.buckets =
        result.data && Array.isArray(result.data.buckets)
          ? result.data.buckets
          : [];
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch token throughput:", e);
    } finally {
      this.#inFlight = false;
      // A granularity change during the fetch was swallowed by the in-flight
      // guard; fetch the now-selected granularity.
      if (this.active && this.granularity !== requested) {
        this.fetch();
      }
    }
  }

  // --- Usage SSE signal stream ---

  async #startUsageSignalStream() {
    await runtimeConfig.ensureLoaded();
    if (!this.active) return;
    if (!runtimeConfig.liveLogsVisible()) return;
    if (typeof ReadableStream === "undefined") return;
    if (this.#sseController) {
      this.#sseController.abort();
    }
    this.#sseController = new AbortController();
    this.#readStream(this.#sseController);
  }

  async #readStream(controller) {
    try {
      const res = await apiFetch("/admin/live/logs?types=usage", {
        signal: controller.signal,
      });
      if (!res.ok || !res.body || typeof res.body.getReader !== "function") {
        this.#scheduleReconnect();
        return;
      }
      this.#reconnectAttempts = 0;
      await consumeEventStream(res.body.getReader(), (event) =>
        this.#handleEvent(event),
      );
      this.#scheduleReconnect();
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Live usage stream failed:", e);
      this.#scheduleReconnect();
    }
  }

  #handleEvent(event) {
    if (!event || typeof event !== "object") return;
    const type = String(event.type || "").trim();
    if (type.indexOf("usage.") === 0) {
      this.noteUsageEvent(type);
    }
  }

  #scheduleReconnect() {
    if (!this.active) return;
    if (this.#reconnectTimer) return;
    const { attempt, delay } = nextReconnect(this.#reconnectAttempts);
    this.#reconnectAttempts = attempt;
    this.#reconnectTimer = setTimeout(() => {
      this.#reconnectTimer = null;
      this.#startUsageSignalStream();
    }, delay);
  }
}

export const liveTokensState = new LiveTokensState();
