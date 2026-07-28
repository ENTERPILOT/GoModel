// Clipboard helper with an execCommand fallback for non-secure contexts.

export function writeTextToClipboard(value: unknown): Promise<void> {
  const payload = String(value == null ? "" : value);
  const clipboard =
    typeof navigator !== "undefined" ? navigator.clipboard : null;

  if (clipboard && typeof clipboard.writeText === "function") {
    return clipboard.writeText(payload);
  }

  const doc = typeof document !== "undefined" ? document : null;
  if (!doc || !doc.body || typeof doc.execCommand !== "function") {
    return Promise.reject(new Error("Clipboard API unavailable"));
  }

  const textarea = doc.createElement("textarea");
  textarea.value = payload;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.opacity = "0";

  try {
    doc.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    if (!doc.execCommand("copy")) {
      throw new Error("execCommand copy returned false");
    }
  } finally {
    if (textarea.parentNode) {
      textarea.parentNode.removeChild(textarea);
    }
  }

  return Promise.resolve();
}

export interface CopyState {
  readonly copied: boolean;
  readonly error: boolean;
  reset(): void;
  copy(value: unknown, formatValue?: (value: unknown) => string): Promise<void>;
}

// createCopyState returns reactive-friendly copy feedback state. Use from a
// component: const copy = createCopyState(); await copy.copy(value).
export function createCopyState(
  { resetDelayMs = 2000, logPrefix }: { resetDelayMs?: number; logPrefix?: string } = {},
): CopyState {
  const state = $state({ copied: false, error: false });
  let resetTimer: ReturnType<typeof setTimeout> | null = null;

  function clearResetTimer() {
    if (resetTimer !== null) {
      clearTimeout(resetTimer);
    }
    resetTimer = null;
  }

  function scheduleReset() {
    clearResetTimer();
    resetTimer = setTimeout(() => {
      state.copied = false;
      state.error = false;
      resetTimer = null;
    }, resetDelayMs);
  }

  return {
    get copied() {
      return state.copied;
    },
    get error() {
      return state.error;
    },
    reset() {
      clearResetTimer();
      state.copied = false;
      state.error = false;
    },
    async copy(value: unknown, formatValue?: (value: unknown) => string) {
      if (value == null || value === "") {
        return;
      }
      clearResetTimer();
      state.copied = false;
      state.error = false;
      try {
        const payload =
          typeof formatValue === "function" ? formatValue(value) : String(value);
        await writeTextToClipboard(payload);
        state.copied = true;
        state.error = false;
      } catch (error) {
        console.error(logPrefix || "Failed to copy text:", error);
        state.copied = false;
        state.error = true;
      }
      scheduleReset();
    },
  };
}
