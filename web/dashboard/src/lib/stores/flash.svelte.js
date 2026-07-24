// Global flash messages (toasts) for transient mutation feedback
// ("Budget saved.", "Unable to delete rate limit."). Rendered once by
// FlashMessages.svelte in App.svelte. Persistent page state — load
// failures, capability warnings, in-form validation errors — stays
// inline on the page; only fire-and-forget feedback belongs here.

const SUCCESS_TIMEOUT_MS = 5000;
const ERROR_TIMEOUT_MS = 8000;

class FlashStore {
  toasts = $state([]);

  #seq = 0;
  #timers = new Map();

  success(message) {
    this.#push("success", message, SUCCESS_TIMEOUT_MS);
  }

  error(message) {
    this.#push("error", message, ERROR_TIMEOUT_MS);
  }

  dismiss(id) {
    const timer = this.#timers.get(id);
    if (timer) {
      clearTimeout(timer);
      this.#timers.delete(id);
    }
    this.toasts = this.toasts.filter((toast) => toast.id !== id);
  }

  #push(kind, message, timeoutMs) {
    const text = String(message || "").trim();
    if (!text) {
      return;
    }
    // An identical toast still on screen restarts instead of stacking
    // (double-clicked saves, repeated failures of the same action).
    const existing = this.toasts.find(
      (toast) => toast.kind === kind && toast.text === text,
    );
    if (existing) {
      this.dismiss(existing.id);
    }
    const id = ++this.#seq;
    this.toasts = [...this.toasts, { id, kind, text }];
    this.#timers.set(
      id,
      setTimeout(() => this.dismiss(id), timeoutMs),
    );
  }
}

export const flash = new FlashStore();
