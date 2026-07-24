// Pure tagging-settings logic.
// Credential-header rejection stays server-side; the PUT surfaces its message.

export function defaultTaggingHeader() {
  return {
    header: "",
    prefix: "",
    do_not_pass: false,
    delimiter: "",
    managed: false,
  };
}

// normalizeTaggingHeaders maps a GET/PUT response payload onto editor rows.
// The default delimiter "," is normalized to "" so the input shows its
// placeholder instead of the implicit default.
export function normalizeTaggingHeaders(payload) {
  const headers =
    payload && Array.isArray(payload.headers) ? payload.headers : [];
  return headers.map((rule) => ({
    header: typeof rule.header === "string" ? rule.header : "",
    prefix: typeof rule.prefix === "string" ? rule.prefix : "",
    do_not_pass: rule.do_not_pass === true,
    delimiter:
      typeof rule.delimiter === "string" && rule.delimiter !== ","
        ? rule.delimiter
        : "",
    managed: rule.managed === true,
  }));
}

// taggingSettingsPayload builds the PUT body: managed (declarative) rows and
// rows without a header name are dropped; header names are trimmed.
export function taggingSettingsPayload(taggingHeaders) {
  return {
    headers: (Array.isArray(taggingHeaders) ? taggingHeaders : [])
      .filter((rule) => !rule.managed && rule.header.trim() !== "")
      .map((rule) => ({
        header: rule.header.trim(),
        prefix: rule.prefix,
        do_not_pass: rule.do_not_pass,
        delimiter: rule.delimiter,
      })),
  };
}

// taggingErrorMessage extracts the server-side rejection message
// ({error: {message}}) from a parsed error payload.
export function taggingErrorMessage(payload) {
  if (payload && payload.error && payload.error.message) {
    return payload.error.message;
  }
  return "";
}
