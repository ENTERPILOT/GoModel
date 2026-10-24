// What the sidebar header shows for a distribution's license: its badge
// (e.g. "PRO") and, for a trial or a license close to its end, a chip with
// the days left. The gateway reports the license through the LICENSE_*
// runtime-config keys; open-source builds report none and show nothing.

const DAY_MS = 24 * 60 * 60 * 1000;

// A full license is counted down only from this many days before it ends; a
// trial always is.
export const EXPIRY_NOTICE_DAYS = 21;
// Within this many days the countdown turns to the warning color.
export const WARNING_DAYS = 7;

// The last day a time covers, as YYYY-MM-DD in UTC: a term that ends at
// midnight ends on the day before it.
function lastDay(ms) {
  return new Date(ms - 1).toISOString().slice(0, 10);
}

function parseTime(value) {
  const ms = Date.parse(String(value || ""));
  return Number.isFinite(ms) ? ms : null;
}

// licenseIndicator returns null when there is no license to show, otherwise
// { badge, chip }. chip is null or { kind, days, tone, endDate }, where
// endDate is the last day of the term (YYYY-MM-DD, UTC):
//   kind "trial" | "expiring": days left until the term ends
//   kind "grace": the term has ended and the license still works; days is
//     how long ago it ended, graceDays how much of the grace period is left,
//     and graceEndDate its last day
//   kind "expired": the license no longer works; days is how long ago it ended
// tone is "normal", "warning" or "danger".
export function licenseIndicator(config, now = Date.now()) {
  const badge = String(config?.LICENSE_BADGE || "").trim();
  if (!badge) return null;

  const trial =
    String(config?.LICENSE_TRIAL || "")
      .trim()
      .toLowerCase() === "on";
  const expiresAt = parseTime(config?.LICENSE_EXPIRES_AT);
  if (expiresAt === null) return { badge, chip: null };
  const validUntil = Math.max(
    expiresAt,
    parseTime(config?.LICENSE_VALID_UNTIL) ?? expiresAt,
  );

  if (now < expiresAt) {
    const days = Math.ceil((expiresAt - now) / DAY_MS);
    if (!trial && days > EXPIRY_NOTICE_DAYS) return { badge, chip: null };
    return {
      badge,
      chip: {
        kind: trial ? "trial" : "expiring",
        days,
        tone: days <= WARNING_DAYS ? "warning" : "normal",
        endDate: lastDay(expiresAt),
      },
    };
  }

  const days = Math.max(1, Math.ceil((now - expiresAt) / DAY_MS));
  if (now < validUntil) {
    return {
      badge,
      chip: {
        kind: "grace",
        days,
        graceDays: Math.ceil((validUntil - now) / DAY_MS),
        graceEndDate: lastDay(validUntil),
        tone: "danger",
        endDate: lastDay(expiresAt),
      },
    };
  }
  return {
    badge,
    chip: { kind: "expired", days, tone: "danger", endDate: lastDay(expiresAt) },
  };
}
