// API-key auth state. The key is stored in localStorage and sent as an
// Authorization bearer header on every admin request. A 401 flips needsAuth
// and opens the dialog; authGeneration invalidates responses that were issued
// under an older key so they cannot clobber fresh data ("stale auth").

const API_KEY_STORAGE_KEY = "gomodel_api_key";

export function normalizeApiKey(value: unknown): string {
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
  generation = $state(0);
  // Incremented whenever the whole dashboard should re-fetch (key change,
  // timezone change, runtime refresh). Pages watch this in an $effect.
  refreshTick = $state(0);

  init(): void {
    try {
      this.apiKey = normalizeApiKey(
        localStorage.getItem(API_KEY_STORAGE_KEY) || "",
      );
    } catch {
      this.apiKey = "";
    }
  }

  hasApiKey(): boolean {
    return normalizeApiKey(this.apiKey) !== "";
  }

  save(): void {
    this.apiKey = normalizeApiKey(this.apiKey);
    try {
      localStorage.setItem(API_KEY_STORAGE_KEY, this.apiKey);
    } catch {
      // Keep the in-memory key when storage is unavailable.
    }
  }

  openDialog(): void {
    this.dialogOpen = true;
  }

  closeDialog(): void {
    this.dialogOpen = false;
  }

  // submit applies a newly entered key and triggers a global refresh.
  submit(): boolean {
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

  refresh(): void {
    this.refreshTick++;
  }

  // handleUnauthorized reports whether the 401/403 belongs to the current
  // key. Stale responses (older generation) are ignored silently. An optional
  // message replaces the generic dialog error text (e.g. a valid key that
  // lacks dashboard access).
  handleUnauthorized(requestGeneration?: number, message = ""): boolean {
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
