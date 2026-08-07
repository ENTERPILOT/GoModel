export const API_KEY_STORAGE_KEY = "gomodel_api_key";

export function readStoredApiKey(storage = globalThis.localStorage) {
  try {
    return storage?.getItem(API_KEY_STORAGE_KEY) || "";
  } catch {
    return "";
  }
}

export function writeStoredApiKey(value, storage = globalThis.localStorage) {
  try {
    storage?.setItem(API_KEY_STORAGE_KEY, value);
  } catch {
    // Keep the in-memory key when storage is unavailable.
  }
}

export function clearStoredApiKey(storage = globalThis.localStorage) {
  try {
    storage?.removeItem(API_KEY_STORAGE_KEY);
  } catch {
    // The in-memory key is still cleared by the caller.
  }
}
