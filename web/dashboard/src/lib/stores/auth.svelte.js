// API-key auth state. The key is stored in localStorage and sent as an
// Authorization bearer header on every admin request. A 401 flips needsAuth
// and opens the dialog; authGeneration invalidates responses that were issued
// under an older key so they cannot clobber fresh data ("stale auth").

import { authenticationResponseMetadata } from "./external-auth.js";

const API_KEY_STORAGE_KEY = "gomodel_api_key";

export function normalizeApiKey(value) {
  const key = String(value || "").trim();
  if (/^Bearer\s*$/i.test(key)) {
    return "";
  }
  const match = key.match(/^Bearer\s+(.+)$/i);
  return match ? match[1].trim() : key;
}

class AuthStore {
  apiKey = $state("");
  needsAuth = $state(false);
  authError = $state(false);
  // Optional specific error text for the auth dialog; empty = generic copy.
  authErrorMessage = $state("");
  dialogOpen = $state(false);
  // Provider-neutral browser auth metadata advertised by an extension.
  externalLoginURL = $state("");
  externalLogoutURL = $state("");
  externalUser = $state("");
  generation = $state(0);
  // Incremented whenever the whole dashboard should re-fetch (key change,
  // timezone change, runtime refresh). Pages watch this in an $effect.
  refreshTick = $state(0);

  init() {
    try {
      this.apiKey = normalizeApiKey(
        localStorage.getItem(API_KEY_STORAGE_KEY) || "",
      );
    } catch {
      this.apiKey = "";
    }
  }

  hasApiKey() {
    return normalizeApiKey(this.apiKey) !== "";
  }

  save() {
    this.apiKey = normalizeApiKey(this.apiKey);
    try {
      localStorage.setItem(API_KEY_STORAGE_KEY, this.apiKey);
    } catch {
      // Keep the in-memory key when storage is unavailable.
    }
  }

  openDialog() {
    this.dialogOpen = true;
  }

  closeDialog() {
    this.dialogOpen = false;
  }

  // submit applies a newly entered key and triggers a global refresh.
  submit() {
    const apiKey = normalizeApiKey(this.apiKey);
    if (!apiKey) {
      this.apiKey = "";
      this.authError = true;
      this.authErrorMessage = "";
      this.needsAuth = true;
      this.openDialog();
      return false;
    }
    this.apiKey = apiKey;
    this.save();
    this.generation++;
    this.authError = false;
    this.authErrorMessage = "";
    this.needsAuth = false;
    this.closeDialog();
    this.refresh();
    return true;
  }

  refresh() {
    this.refreshTick++;
  }

  observeResponse(response) {
    const metadata = authenticationResponseMetadata(response);
    if (!metadata) return;
    if (Object.hasOwn(metadata, "loginURL")) {
      this.externalLoginURL = metadata.loginURL;
    }
    if (Object.hasOwn(metadata, "logoutURL")) {
      this.externalLogoutURL = metadata.logoutURL;
    }
    if (Object.hasOwn(metadata, "user")) {
      this.externalUser = metadata.user;
    }
  }

  // handleUnauthorized reports whether the 401/403 belongs to the current
  // key. Stale responses (older generation) are ignored silently. An optional
  // message replaces the generic dialog error text (e.g. a valid key that
  // lacks dashboard access).
  handleUnauthorized(requestGeneration, message = "") {
    if (
      typeof requestGeneration === "number" &&
      requestGeneration < this.generation
    ) {
      return false;
    }
    this.authError = true;
    this.authErrorMessage = message;
    this.needsAuth = true;
    this.openDialog();
    return true;
  }
}

export const auth = new AuthStore();
