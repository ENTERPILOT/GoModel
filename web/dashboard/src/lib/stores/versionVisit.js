// Pure helpers for the daily update-check gate. The gateway sets a
// `gomodel_version_check` cookie whose value is `YYYY-MM-DD-{id}`: the day
// this browser last checked plus a random id minted on the first visit. It is
// deliberately readable from JavaScript so the dashboard can skip the request
// on every page load after the first one each day.

export const VISIT_COOKIE = "gomodel_version_check";

const DATE_LENGTH = "2026-08-26".length;

/** localDateKey returns a date in the browser's own timezone as YYYY-MM-DD. */
export function localDateKey(now = new Date()) {
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${now.getFullYear()}-${month}-${day}`;
}

/**
 * readVisitCookie pulls the visit marker out of a `document.cookie` string,
 * returning "" when it is absent.
 */
export function readVisitCookie(cookieHeader = "") {
  const prefix = `${VISIT_COOKIE}=`;
  const match = String(cookieHeader)
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(prefix));
  if (!match) return "";
  try {
    return decodeURIComponent(match.slice(prefix.length));
  } catch {
    // A cookie value that is not valid percent-encoding is unusable but not
    // worth throwing over; treat it as absent and check again.
    return "";
  }
}

/**
 * checkedToday mirrors the backend's gate: a cookie whose date half is today
 * and that carries an id means this browser has already checked in. Anything
 * missing or malformed counts as due, so a corrupted cookie self-heals.
 */
export function checkedToday(cookieValue, today = localDateKey()) {
  const value = String(cookieValue || "");
  if (value.length <= DATE_LENGTH) return false;
  return value.slice(0, DATE_LENGTH) === today && value[DATE_LENGTH] === "-";
}

/**
 * checkPlan decides how the dashboard should ask for the update status.
 *
 * `fetch` is unconditionally true, and that is the point: GET /version is a
 * local request answered from the gateway's cache, and its result is what
 * renders the update indicator. Gating the request on the cookie once meant
 * the indicator appeared on the day's first page load and vanished for every
 * load after it.
 *
 * The cookie decides only `delayed`. When this browser is due, the request may
 * make the gateway call the release host, so it is spread over a random pause;
 * when it is not due, nothing outbound can happen and the status should paint
 * immediately.
 */
export function checkPlan(cookieValue, today = localDateKey()) {
  return { fetch: true, delayed: !checkedToday(cookieValue, today) };
}
