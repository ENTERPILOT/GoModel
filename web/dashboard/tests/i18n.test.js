import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";

import * as m from "../src/lib/paraglide/messages.js";
import {
  baseLocale,
  getTextDirection,
  locales,
  strategy,
} from "../src/lib/paraglide/runtime.js";

const catalogPath = fileURLToPath(
  new URL("../messages/en.json", import.meta.url),
);
const { $schema, ...englishMessages } = JSON.parse(
  readFileSync(catalogPath, "utf8"),
);

test("the English source catalog contains valid flat semantic keys", () => {
  assert.equal($schema, "https://inlang.com/schema/inlang-message-format");
  assert.ok(Object.keys(englishMessages).length > 0);

  for (const [key, message] of Object.entries(englishMessages)) {
    assert.match(key, /^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$/);
    assert.ok(
      (typeof message === "string" && message.trim() !== "") ||
        (Array.isArray(message) && message.length > 0),
      `${key} must contain a translation`,
    );
  }
});

test("Paraglide compiles interpolation and locale-aware plurals", () => {
  assert.equal(
    m.pagination_summary({ start: 1, end: 25, total: 80 }),
    "Showing 1–25 of 80",
  );
  assert.equal(m.date_picker_last_days({ count: 1 }), "Last 1 day");
  assert.equal(m.date_picker_last_days({ count: 14 }), "Last 14 days");
  assert.equal(m.date_picker_days({ count: 1 }), "1 day");
  assert.equal(
    m.pagination_summary(
      { start: 1, end: 25, total: 80 },
      { locale: "pl" },
    ),
    "Wyświetlanie 1–25 z 80",
  );
  assert.equal(
    m.date_picker_last_days({ count: 1 }, { locale: "pl" }),
    "Ostatni 1 dzień",
  );
  assert.equal(
    m.date_picker_last_days({ count: 2 }, { locale: "pl" }),
    "Ostatnie 2 dni",
  );
  assert.equal(
    m.date_picker_last_days({ count: 5 }, { locale: "pl" }),
    "Ostatnie 5 dni",
  );
  assert.equal(
    m.date_picker_days({ count: 1 }, { locale: "pl" }),
    "1 dzień",
  );
  assert.equal(
    m.date_picker_days({ count: 2 }, { locale: "pl" }),
    "2 dni",
  );
  assert.equal(
    m.date_picker_days({ count: 5 }, { locale: "pl" }),
    "5 dni",
  );
  assert.equal(
    m.audit_provider_attempts({ count: 1 }, { locale: "pl" }),
    "1 próba dostawcy",
  );
  assert.equal(
    m.audit_provider_attempts({ count: 2 }, { locale: "pl" }),
    "2 próby dostawców",
  );
  assert.equal(
    m.audit_provider_attempts({ count: 5 }, { locale: "pl" }),
    "5 prób dostawców",
  );
  assert.equal(
    m.settings_pricing_summary(
      { matched: 1, recalculated: 1 },
      { locale: "pl" },
    ),
    "Przeliczono ceny dla 1 z 1 rekordu zużycia.",
  );
  assert.equal(
    m.settings_pricing_missing({ count: 2 }, { locale: "pl" }),
    "2 rekordy zużycia nadal nie mają metadanych cen.",
  );
  assert.equal(
    m.settings_pricing_missing({ count: 5 }, { locale: "pl" }),
    "5 rekordów zużycia nadal nie ma metadanych cen.",
  );
  assert.equal(
    m.settings_runtime_refresh_models({ count: 2 }, { locale: "pl" }),
    "2 modele",
  );
  assert.equal(
    m.settings_runtime_refresh_providers({ count: 5 }, { locale: "pl" }),
    "5 dostawców",
  );
  assert.equal(
    m.settings_pricing_confirmation({}, { locale: "pl" }),
    "przelicz",
  );
  assert.equal(
    m.providers_keys_count({ count: 1 }, { locale: "pl" }),
    "1 klucz",
  );
  assert.equal(
    m.providers_keys_count({ count: 2 }, { locale: "pl" }),
    "2 klucze",
  );
  assert.equal(
    m.providers_keys_count({ count: 5 }, { locale: "pl" }),
    "5 kluczy",
  );
  assert.equal(
    m.providers_models_count({ count: 5 }, { locale: "pl" }),
    "5 modeli",
  );
  assert.equal(
    m.models_count({ count: 5 }, { locale: "pl" }),
    "5 modeli",
  );
  assert.equal(
    m.models_alias_count({ count: 5 }, { locale: "pl" }),
    "5 aliasów",
  );
  assert.equal(
    m.overview_total_requests({}, { locale: "pl" }),
    "Łączna liczba Request'ów",
  );
  assert.equal(
    m.overview_live_token_throughput({}, { locale: "pl" }),
    "Przepływ tokenów",
  );
  assert.equal(
    m.overview_provider_status({}, { locale: "pl" }),
    "Status provider'ów",
  );
  assert.equal(
    m.overview_input_tokens({}, { locale: "pl" }),
    "Input Tokens",
  );
  assert.equal(m.audit_filter_field_search(), "All fields");
  assert.equal(
    m.audit_filter_field_search({}, { locale: "pl" }),
    "Wszystkie pola",
  );
  assert.equal(m.rate_limits_title({}, { locale: "pl" }), "Rate Limits");
});

test("the browser locale strategy persists overrides without changing routes", () => {
  assert.equal(baseLocale, "en");
  assert.deepEqual(locales, ["en", "pl"]);
  assert.deepEqual(strategy, [
    "custom-dashboard",
    "preferredLanguage",
    "baseLocale",
  ]);
  assert.equal(getTextDirection("en"), "ltr");
  assert.equal(getTextDirection("ar"), "rtl");
});

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "paraglide" ? [] : sourceFiles(path);
    }
    return [".js", ".svelte"].includes(extname(path)) ? [path] : [];
  });
}

test("every English message is referenced by dashboard source", () => {
  const sourceRoot = fileURLToPath(new URL("../src", import.meta.url));
  const source = sourceFiles(sourceRoot)
    .map((path) => readFileSync(path, "utf8"))
    .join("\n");

  for (const key of Object.keys(englishMessages)) {
    assert.ok(
      source.includes(`m.${key}`),
      `${key} is not used by dashboard source`,
    );
  }
});
