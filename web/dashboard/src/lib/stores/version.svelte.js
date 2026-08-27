// Update awareness. The gateway owns the outbound check; the dashboard only
// asks GET /version for its result and renders it.
//
// GET /version is always called, on every page load: it is a local request
// that answers from the gateway's cache, and the result has to be in hand for
// the update indicator to render at all. The once-a-day gate belongs to the
// *outbound* call to the release host, and the backend owns it — it reads the
// `gomodel_version_check` cookie and only goes out to the network when this
// browser has not checked in today.
//
// The one thing the cookie decides here is timing: when this browser is due,
// the request may trigger that outbound call, so it is delayed by a random
// pause to keep a fleet of dashboards opened after a rollout from arriving at
// the release host in one burst. When it is not due, nothing outbound can
// happen, so the request goes immediately and the indicator paints at once.

import { gomodelPath } from "$lib/api/paths.js";
import { checkPlan, readVisitCookie } from "./versionVisit.js";

// Upper bound on the random pause before the daily check, in milliseconds.
const MAX_START_DELAY_MS = 20000;
const REQUEST_TIMEOUT_MS = 10000;

// Release notes are published only for the open-source gateway. GoModel Pro
// has no public release notes, and a custom distribution's releases are not
// this page's to link to — so the link is offered for open core alone, never
// by ruling distributions out one at a time.
const CORE_APP = "GoModel";
const CORE_RELEASES_URL = "https://github.com/ENTERPILOT/GoModel/releases";

class VersionStore {
  /** The gateway's own version, e.g. "0.1.81". */
  current = $state("");
  /** The newest published release, when a check has succeeded. */
  latest = $state("");
  /** Whether `latest` is newer than `current`. */
  updateAvailable = $state(false);
  /** The distribution name: "GoModel" or "GoModel Pro". */
  app = $state("");

  /**
   * dismissed hides the update notice for this page session only. It is
   * deliberately not persisted: an update stays available until it is
   * installed, so the notice must come back on the next load rather than
   * being silenced for good by one click.
   */
  dismissed = $state(false);

  /** noticeVisible is what the update panel renders on. */
  get noticeVisible() {
    return this.updateAvailable && !this.dismissed;
  }

  dismissNotice() {
    this.dismissed = true;
  }

  /**
   * releaseNotesURL is the public release notes for this distribution, or ""
   * when it publishes none. Callers render a link only when it is non-empty.
   */
  get releaseNotesURL() {
    return this.app === CORE_APP ? CORE_RELEASES_URL : "";
  }

  #started = false;

  // init runs at most one fetch per page load. Calling it again (a remount, a
  // second page) is a no-op.
  init() {
    if (this.#started || typeof window === "undefined") return;
    this.#started = true;
    const cookie = readVisitCookie(
      typeof document === "undefined" ? "" : document.cookie,
    );
    const plan = checkPlan(cookie);
    if (!plan.delayed) {
      void this.#check();
      return;
    }
    window.setTimeout(
      () => void this.#check(),
      Math.random() * MAX_START_DELAY_MS,
    );
  }

  async #check() {
    const controller =
      typeof AbortController === "function" ? new AbortController() : null;
    const timeoutID = controller
      ? setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
      : null;
    try {
      // A plain fetch on purpose: /version needs no credentials, and the
      // admin client would attach the API key to a request that does not want
      // one. `credentials: same-origin` still sends and stores the visit
      // cookie the backend uses for its own gate.
      const res = await fetch(gomodelPath("/version"), {
        credentials: "same-origin",
        signal: controller ? controller.signal : undefined,
      });
      if (!res.ok) return;
      const data = await res.json();
      this.app = String(data?.app || "");
      this.current = String(data?.version || "");
      this.latest = String(data?.latest || "");
      this.updateAvailable = data?.update_available === true;
      // A newly discovered release re-opens a notice hidden earlier in this
      // session, so a dismissal never carries over to a different version.
      this.dismissed = false;
    } catch {
      // An unreachable release host is not a dashboard problem: the gateway
      // keeps working and the check retries tomorrow.
    } finally {
      if (timeoutID !== null) clearTimeout(timeoutID);
    }
  }
}

export const versionStore = new VersionStore();
