// Admin-API error extraction. Kept free of Svelte-runtime imports so pure
// page-logic modules (and their node:test suites) can use it directly;
// $lib/api/client.ts re-exports both helpers for component/store code.

// errorPayloadMessage pulls {error:{message}} out of a parsed admin payload.
export function errorPayloadMessage(data: unknown, fallback: string): string {
  const error =
    data && typeof data === "object"
      ? (data as { error?: unknown }).error
      : null;
  const message =
    error && typeof error === "object"
      ? (error as { message?: unknown }).message
      : null;
  const trimmed = typeof message === "string" ? message.trim() : "";
  return trimmed || fallback;
}

// errorMessage does the same for a getJSON/sendJSON result envelope, also
// accepting the flatter {message} / {error: "..."} shapes.
export function errorMessage(
  result: { data?: unknown } | null | undefined,
  fallback: string,
): string {
  const data = result && result.data;
  if (data && typeof data === "object") {
    const payload = data as { message?: unknown; error?: unknown };
    const candidates = [
      payload.message,
      payload.error,
      payload.error && typeof payload.error === "object"
        ? (payload.error as { message?: unknown }).message
        : null,
    ];
    for (const msg of candidates) {
      if (typeof msg === "string" && msg.trim()) {
        return msg.trim();
      }
    }
  }
  return fallback;
}
