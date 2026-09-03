// Access scope of the dashboard credential, from GET /admin/access. A
// managed key bound to a user path only administers that subtree: pages that
// expose gateway-wide configuration are hidden (see navigation.js), lists
// arrive filtered from the server, and editors default their user path to
// the scope root. A missing endpoint or a failed load means global, so older
// backends keep the full dashboard. Pages that must not fire global-only
// requests wait for `loaded` before fetching.

import { getJSON } from "$lib/api/client.js";
import { parseAccessScope, scopeAllows } from "./accessScope.js";

class AccessStore {
  scope = $state("global");
  userPath = $state("");
  loaded = $state(false);
  #inflight = null;

  get scoped() {
    return this.scope === "user_path";
  }

  // allows reports whether a user path lies inside the credential's scope.
  allows(path) {
    return !this.scoped || scopeAllows(this.userPath, path);
  }

  // defaultPath returns the value to prefill a user-path field with: the
  // scope root under a scoped credential, the given fallback otherwise.
  defaultPath(fallback = "") {
    return this.scoped ? this.userPath : fallback;
  }

  fetch() {
    if (this.#inflight) {
      return this.#inflight;
    }
    this.loaded = false;
    this.#inflight = this.#load().finally(() => {
      this.#inflight = null;
    });
    return this.#inflight;
  }

  async ensureLoaded() {
    if (this.#inflight) {
      await this.#inflight;
      return;
    }
    if (this.loaded) {
      return;
    }
    await this.fetch();
  }

  async #load() {
    let parsed = parseAccessScope(null);
    try {
      const result = await getJSON("/admin/access", { label: "access scope" });
      if (result.stale) {
        // The key changed while this request was in flight: the answer
        // belongs to the old credential, so load again for the new one.
        return this.#load();
      }
      if (result.ok) {
        parsed = parseAccessScope(result.data);
      }
    } catch (e) {
      console.error("Failed to fetch access scope:", e);
    }
    this.scope = parsed.scope;
    this.userPath = parsed.userPath;
    this.loaded = true;
  }
}

export const access = new AccessStore();
