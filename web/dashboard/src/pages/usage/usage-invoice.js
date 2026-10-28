// Invoice export (GoModel Pro): GET /admin/usage/invoice returns billed usage
// per customer as a CSV attachment. Pure helpers: node:test exercises them
// without a DOM.

const FALLBACK_FILENAME = "gomodel-invoice.csv";

// invoiceExportVisible reports whether the Usage page offers the export: the
// entitlement unlocks it and it needs billed cost, which markups provide.
export function invoiceExportVisible(config) {
  return Boolean(config && config.invoiceExportVisible() && config.billedCostVisible());
}

// invoiceExportPath builds the CSV request for the page's window and filters
// (an admin API query without "?").
export function invoiceExportPath(apiQuery) {
  const qs = String(apiQuery || "").replace(/^[?&]+/, "");
  return "/admin/usage/invoice?format=csv" + (qs ? "&" + qs : "");
}

// invoiceFilename reads the attachment name from a Content-Disposition
// header, keeping only a plain file name.
export function invoiceFilename(contentDisposition) {
  const match = /filename="?([^";]+)"?/i.exec(String(contentDisposition || ""));
  const name = match ? match[1].trim() : "";
  return /^[\w.-]+\.csv$/.test(name) ? name : FALLBACK_FILENAME;
}
