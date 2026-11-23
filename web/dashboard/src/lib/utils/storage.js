// localStorage access that never throws: it is absent in SSR/node contexts and
// blocked outright by some privacy settings, so every caller must cope with
// null rather than guard at each call site.

export function browserStorage() {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
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

/**
 * Read a key that used to be stored as `legacyKey`. A value found only under
 * the old name is returned and copied to the new one, so the next read no
 * longer needs it. The old key is left in place: a dashboard served by a
 * gateway from before the rename still reads it.
 */
export function readStoredRenamed(key, legacyKey, fallback = null) {
  const value = readStored(key);
  if (value != null) return value;
  const legacy = readStored(legacyKey);
  if (legacy == null) return fallback;
  writeStored(key, legacy);
  return legacy;
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
