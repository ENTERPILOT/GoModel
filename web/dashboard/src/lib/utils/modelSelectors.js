// Model selector list helpers shared by the API Keys and Users pages.
// An allowlist is entered as free text (comma- or newline-separated) and sent
// to the backend as an array; the backend canonicalizes "anthropic/*" to
// "anthropic/" and "*" to "/", so the UI shows the wildcard forms back.

// parseModelSelectors splits free text (or an array of entries, each of
// which may itself hold comma-separated values) into a trimmed,
// de-duplicated list (order preserved).
export function parseModelSelectors(value) {
  const text = Array.isArray(value) ? value.join(",") : String(value || "");
  const selectors = [];
  for (const piece of text.split(/[,\n]/)) {
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

// modelSelectorOptions builds SearchSelect options from the shared model
// inventory: one provider-wide wildcard per provider first, then every
// concrete selector, both sorted. `describeProvider(name)` supplies the
// wildcard's description so this module stays free of message imports.
export function modelSelectorOptions(models, describeProvider = () => "") {
  const list = Array.isArray(models) ? models : [];
  const providers = [...new Set(list.map((entry) => entry?.provider_name).filter(Boolean))].sort();
  const selectors = [
    ...new Set(list.map((entry) => entry?.access?.selector || entry?.selector).filter(Boolean)),
  ].sort();
  return [
    ...providers.map((name) => ({ value: name + "/*", label: name + "/*", description: describeProvider(name) })),
    ...selectors.map((selector) => ({ value: selector, label: selector, description: "" })),
  ];
}
