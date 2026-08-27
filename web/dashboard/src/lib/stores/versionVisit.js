// Pure helpers for the daily update-check gate. The gateway sets a
// `gomodel_version_check` cookie whose value is `YYYY-MM-DD-{id}`: the day
// this browser last checked plus a random id minted on the first visit. It is
// deliberately readable from JavaScript so the dashboard can skip the request
// on every page load after the first one each day.

export const VISIT_COOKIE = "gomodel_version_check";

const DATE_LENGTH = "2026-08-26".length;

// The gateway mints and accepts only this shape, and discards anything else
// as a first visit. Matching it here keeps the two sides from disagreeing
// about whether this browser has already checked in.
const CANONICAL_ID =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/**
 * utcDateKey returns a date as YYYY-MM-DD in UTC.
 *
 * UTC, not local time: the gateway stamps the cookie in UTC, and a browser in
 * a different timezone comparing local dates would disagree with it about when
 * a new day begins.
 */
export function utcDateKey(now = new Date()) {
  return now.toISOString().slice(0, 10);
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
export function checkedToday(cookieValue, today = utcDateKey()) {
  const value = String(cookieValue || "");
  if (value.length <= DATE_LENGTH + 1) return false;
  if (value.slice(0, DATE_LENGTH) !== today || value[DATE_LENGTH] !== "-") {
    return false;
  }
  return CANONICAL_ID.test(value.slice(DATE_LENGTH + 1));
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
export function checkPlan(cookieValue, today = utcDateKey()) {
  return { fetch: true, delayed: !checkedToday(cookieValue, today) };
}
