// localStorage access that never throws: it is absent in SSR/node contexts and
// blocked outright by some privacy settings, so every caller must cope with
// null rather than guard at each call site.

export function browserStorage() {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

/** Read a key, returning `fallback` when storage is unavailable or empty. */
export function readStored(key, fallback = null) {
  const storage = browserStorage();
  if (!storage) return fallback;
  try {
    const value = storage.getItem(key);
    return value == null ? fallback : value;
  } catch {
    return fallback;
  }
}

/** Write a key; a failure is non-fatal (the preference just won't persist). */
export function writeStored(key, value) {
  const storage = browserStorage();
  if (!storage) return;
  try {
    storage.setItem(key, String(value));
  } catch {
    // Non-fatal.
  }
}
