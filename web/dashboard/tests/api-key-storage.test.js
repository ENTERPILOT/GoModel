import assert from "node:assert/strict";
import test from "node:test";

import {
  API_KEY_STORAGE_KEY,
  clearStoredApiKey,
  readStoredApiKey,
  writeStoredApiKey,
} from "../src/lib/stores/api-key-storage.js";

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
  };
}

test("stored API key helpers clear a credential when switching to SSO", () => {
  const storage = memoryStorage({ [API_KEY_STORAGE_KEY]: "old-key" });
  assert.equal(readStoredApiKey(storage), "old-key");

  clearStoredApiKey(storage);
  assert.equal(readStoredApiKey(storage), "");

  writeStoredApiKey("new-key", storage);
  assert.equal(readStoredApiKey(storage), "new-key");
});

test("stored API key helpers tolerate unavailable storage", () => {
  const unavailable = {
    getItem() { throw new Error("blocked"); },
    setItem() { throw new Error("blocked"); },
    removeItem() { throw new Error("blocked"); },
  };
  assert.equal(readStoredApiKey(unavailable), "");
  assert.doesNotThrow(() => writeStoredApiKey("key", unavailable));
  assert.doesNotThrow(() => clearStoredApiKey(unavailable));
});
