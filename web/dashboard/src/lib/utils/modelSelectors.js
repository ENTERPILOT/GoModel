// Model selector list helpers shared by the API Keys and Users pages.
// An allowlist is entered as free text (comma- or newline-separated) and sent
// to the backend as an array; the backend canonicalizes "anthropic/*" to
// "anthropic/" and "*" to "/", so the UI shows the wildcard forms back.

// parseModelSelectors splits free text into a trimmed, de-duplicated list
// (order preserved).
export function parseModelSelectors(value) {
  const selectors = [];
  for (const piece of String(value || "").split(/[,\n]/)) {
    const selector = piece.trim();
    if (selector && !selectors.includes(selector)) {
      selectors.push(selector);
    }
  }
  return selectors;
}

// displayModelSelector renders a canonical selector the way operators type
// it: provider-wide "anthropic/" as "anthropic/*", global "/" as "*".
export function displayModelSelector(selector) {
  const value = String(selector || "").trim();
  if (value === "/") {
    return "*";
  }
  if (value.endsWith("/")) {
    return value + "*";
  }
  return value;
}

// formatModelSelectors joins a canonical list into editor text, one per line.
export function formatModelSelectors(selectors) {
  return (Array.isArray(selectors) ? selectors : [])
    .map(displayModelSelector)
    .join("\n");
}
