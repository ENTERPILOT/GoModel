// "Open in Playground": loads a logged request into the Playground and
// navigates there. Request log rows and usage log rows share it; what can be
// replayed, and what is left out, is decided in playgroundSnapshot.js.

import { getJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { playgroundStore } from "./playground.svelte.js";
import {
  playgroundSnapshot,
  requestLogFetchOutcome,
  usageEntryRequestLogQuery,
  usageRequestLogMatch,
} from "./playgroundSnapshot.js";

// The API client already dealt with the response (see requestLogFetchOutcome):
// give up without telling the user anything.
const SILENT = Symbol("silent");
// Several logged requests share the usage row's request id and none can be
// singled out.
const AMBIGUOUS = Symbol("ambiguous");

// Resolves to the response data, null when there is none, or SILENT.
async function fetchRequestLogs(path, label) {
  const result = await getJSON(path, { label });
  const outcome = requestLogFetchOutcome(result);
  if (outcome === "silent") return SILENT;
  if (outcome === "failed") throw new Error(label + ": HTTP " + result.status);
  return outcome === "ok" ? result.data : null;
}

function fetchRequestLogDetail(id) {
  return fetchRequestLogs(
    "/admin/request-logs/detail?log_id=" + encodeURIComponent(String(id || "")),
    "request log detail",
  );
}

async function fetchUsageRequestLogEntry(usageEntry) {
  const page = await fetchRequestLogs(
    "/admin/request-logs?" + usageEntryRequestLogQuery(usageEntry),
    "request log lookup",
  );
  if (page === SILENT) return SILENT;
  const match = usageRequestLogMatch(usageEntry, page?.entries, page?.total);
  if (match.ambiguous) return AMBIGUOUS;
  return match.entry ? fetchRequestLogDetail(match.entry.id) : null;
}

class PlaygroundOpener {
  // Key of the row whose request is being loaded ("" when idle); its button
  // is disabled meanwhile.
  pending = $state("");
  #token = 0;

  requestLogKey(entry) {
    return "request:" + String(entry?.id || "");
  }

  usageKey(entry) {
    return "usage:" + String(entry?.id || "");
  }

  // entry: a request log row. List rows are slim, so their bodies come from
  // the detail endpoint; a row that already holds its body is used as is.
  openRequestLogEntry(entry) {
    return this.#open(this.requestLogKey(entry), () =>
      playgroundSnapshot(entry) ? entry : fetchRequestLogDetail(entry?.id));
  }

  // entry: a usage log row, resolved to its request log entry by request id.
  openUsageEntry(entry) {
    return this.#open(this.usageKey(entry), () => fetchUsageRequestLogEntry(entry));
  }

  // The latest click wins: a slower earlier load never navigates.
  async #open(key, resolve) {
    const token = ++this.#token;
    this.pending = key;
    let entry = null;
    let failed = false;
    try {
      entry = await resolve();
    } catch (error) {
      console.error("Failed to load the request for the Playground:", error);
      failed = true;
    }
    if (token !== this.#token) return;
    this.pending = "";
    if (entry === SILENT) return;
    if (failed) {
      flash.error(m.playground_open_failed());
      return;
    }
    if (entry === AMBIGUOUS) {
      flash.error(m.playground_open_ambiguous());
      return;
    }
    const snapshot = playgroundSnapshot(entry);
    if (!snapshot) {
      flash.error(m.playground_open_unavailable());
      return;
    }
    playgroundStore.loadSnapshot(snapshot);
    if (snapshot.partial) flash.success(m.playground_open_partial());
    router.navigate("playground");
  }
}

export const playgroundOpener = new PlaygroundOpener();
