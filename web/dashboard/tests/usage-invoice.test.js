import test from "node:test";
import assert from "node:assert/strict";

import { invoiceExportPath, invoiceExportVisible, invoiceFilename } from "../src/pages/usage/usage-invoice.js";

test("invoiceExportVisible needs the entitlement and billed cost", () => {
  const config = (invoice, billed) => ({ invoiceExportVisible: () => invoice, billedCostVisible: () => billed });
  assert.equal(invoiceExportVisible(config(true, true)), true);
  assert.equal(invoiceExportVisible(config(true, false)), false);
  assert.equal(invoiceExportVisible(config(false, true)), false);
  assert.equal(invoiceExportVisible(null), false);
});

test("invoiceExportPath asks for CSV with the page window and filters", () => {
  assert.equal(invoiceExportPath(""), "/admin/usage/invoice?format=csv");
  assert.equal(
    invoiceExportPath("model=gpt-5&start_date=2026-03-01&end_date=2026-03-31"),
    "/admin/usage/invoice?format=csv&model=gpt-5&start_date=2026-03-01&end_date=2026-03-31",
  );
  assert.equal(invoiceExportPath("&days=30"), "/admin/usage/invoice?format=csv&days=30");
});

test("invoiceFilename keeps the server's plain CSV name", () => {
  const cases = [
    ['attachment; filename="gomodel-invoice-2026-03-01-to-2026-03-31.csv"', "gomodel-invoice-2026-03-01-to-2026-03-31.csv"],
    ["attachment; filename=invoice.csv", "invoice.csv"],
    ['attachment; filename="../etc/passwd"', "gomodel-invoice.csv"],
    ["", "gomodel-invoice.csv"],
    [null, "gomodel-invoice.csv"],
  ];
  for (const [header, want] of cases) {
    assert.equal(invoiceFilename(header), want, String(header));
  }
});
