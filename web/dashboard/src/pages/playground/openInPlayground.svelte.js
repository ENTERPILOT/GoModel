// "Open in Playground": loads a logged request into the Playground and
// navigates there. Request log rows and usage log rows share it; what can be
// replayed, and what is left out, is decided in playgroundSnapshot.js.

import { getJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { playgroundStore } from "./playground.svelte.js";
import { playgroundSnapshot, usageEntryRequestLogQuery } from "./playgroundSnapshot.js";

// A response for a previous API key, or a 401 (the auth dialog is already
// opening): give up without telling the user anything.
const SILENT = Symbol("silent");

async function fetchRequestLogDetail(id) {
  const result = await getJSON(
    "/admin/request-logs/detail?log_id=" + encodeURIComponent(String(id || "")),
    { label: "request log detail" },
  );
  if (result.stale || result.status === 401) return SILENT;
  if (result.status === 404) return null;
  if (!result.ok) throw new Error("request log detail: HTTP " + result.status);
  return result.data;
}

async function fetchUsageRequestLogEntry(usageEntry) {
  const result = await getJSON(
    "/admin/request-logs?" + usageEntryRequestLogQuery(usageEntry),
    { label: "request log lookup" },
  );
  if (result.stale || result.status === 401) return SILENT;
  if (!result.ok) throw new Error("request log lookup: HTTP " + result.status);
  const found = Array.isArray(result.data?.entries) ? result.data.entries[0] : null;
  return found && found.id ? fetchRequestLogDetail(found.id) : null;
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
