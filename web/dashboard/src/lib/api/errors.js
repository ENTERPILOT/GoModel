// Admin-API error extraction. Kept free of Svelte-runtime imports so pure
// page-logic modules (and their node:test suites) can use it directly;
// $lib/api/client.js re-exports both helpers for component/store code.

// errorPayloadMessage pulls {error:{message}} out of a parsed admin payload.
export function errorPayloadMessage(data, fallback) {
  const message = data && typeof data === "object" && data.error && data.error.message;
  const trimmed = typeof message === "string" ? message.trim() : "";
  return trimmed || fallback;
}

// errorPayloadProvider reads {error:{provider}} — the upstream provider a
// gateway error originated from, "" when the gateway raised it itself.
export function errorPayloadProvider(data) {
  const provider = data && typeof data === "object" && data.error && data.error.provider;
  return typeof provider === "string" ? provider.trim() : "";
}

// isGatewayAuthError tells a rejected gateway credential (the dashboard key
// is wrong or lacks access — reopen the key dialog) from an upstream
// provider rejecting its own key (report it like any other request error).
// A 401 with no readable payload is treated as the gateway's.
export function isGatewayAuthError(data) {
  const error = data && typeof data === "object" ? data.error : null;
  if (!error || typeof error !== "object" || Array.isArray(error)) return true;
  return String(error.type || "") === "authentication_error" && !errorPayloadProvider(data);
}

// errorMessage does the same for a getJSON/sendJSON result envelope, also
// accepting the flatter {message} / {error: "..."} shapes.
export function errorMessage(result, fallback) {
  const data = result && result.data;
  if (data && typeof data === "object") {
    const candidates = [
      data.message,
      data.error,
      data.error && typeof data.error === "object" ? data.error.message : null,
    ];
    for (const msg of candidates) {
      if (typeof msg === "string" && msg.trim()) {
        return msg.trim();
      }
    }
  }
  return fallback;
}
