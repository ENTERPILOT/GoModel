import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  catalogErrors,
  canonicalLocale,
  interpolate,
  localeDirection,
  matchLocale,
  pluralKey,
  translate,
} from "../src/lib/i18n/catalog.js";
import en from "../src/lib/i18n/locales/en.js";
import { LOCALES } from "../src/lib/i18n/locales.js";

test("the English source catalog contains non-empty semantic keys", () => {
  const entries = Object.entries(en);
  assert.ok(entries.length > 0);
  for (const [key, value] of entries) {
    assert.match(key, /^[a-z][A-Za-z0-9]*(\.[a-z][A-Za-z0-9]*)+$/);
    assert.equal(typeof value, "string");
    assert.notEqual(value.trim(), "");
  }
});

test("interpolate replaces named values and preserves missing placeholders", () => {
  assert.equal(
    interpolate("Showing {start}–{end} of {total}", { start: 1, end: 25 }),
    "Showing 1–25 of {total}",
  );
});

test("translate uses the active catalog, English fallback, then the key", () => {
  const messages = { "common.actions.next": "Dalej" };
  assert.equal(translate(messages, en, "common.actions.next"), "Dalej");
  assert.equal(translate(messages, en, "common.actions.cancel"), "Cancel");
  assert.equal(translate(messages, en, "missing.message"), "missing.message");
});

test("every registered locale has valid keys, values, and placeholders", async () => {
  for (const definition of Object.values(LOCALES)) {
    const loaded = await definition.load();
    const messages = loaded.default || loaded;
    assert.deepEqual(catalogErrors(messages, en), []);
  }
});

test("catalog validation rejects unknown keys, blanks, and changed placeholders", () => {
  assert.deepEqual(
    catalogErrors(
      {
        "pagination.summary": "Showing {start}–{total}",
        "common.actions.next": " ",
        "unknown.key": "Unknown",
      },
      en,
    ),
    [
      "pagination.summary: placeholders must be {end}, {start}, {total}",
      "common.actions.next: translation must be a non-empty string",
      "unknown.key: key is not present in the source catalog",
    ],
  );
});

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return [".js", ".svelte"].includes(extname(path)) ? [path] : [];
  });
}

test("every English message is referenced by dashboard source", () => {
  const sourceRoot = fileURLToPath(new URL("../src", import.meta.url));
  const englishCatalog = fileURLToPath(
    new URL("../src/lib/i18n/locales/en.js", import.meta.url),
  );
  const source = sourceFiles(sourceRoot)
    .filter((path) => path !== englishCatalog)
    .map((path) => readFileSync(path, "utf8"))
    .join("\n");

  for (const key of Object.keys(en)) {
    const usageKey = key.replace(/\.(zero|one|two|few|many|other)$/, "");
    assert.ok(
      source.includes(`"${usageKey}"`),
      `${key} is not used by dashboard source`,
    );
  }
});

test("matchLocale prefers exact tags, then the same language, then fallback", () => {
  const supported = ["en", "pt-BR", "pl"];
  assert.equal(matchLocale(["pt-PT"], supported, "en"), "pt-BR");
  assert.equal(matchLocale(["pl-PL"], supported, "en"), "pl");
  assert.equal(matchLocale(["de-DE"], supported, "en"), "en");
  assert.equal(canonicalLocale("PT-br"), "pt-BR");
});

test("pluralKey follows locale plural rules with an other fallback", () => {
  assert.equal(
    pluralKey("en", "items", 1, { "items.one": "one" }, {}),
    "items.one",
  );
  assert.equal(
    pluralKey("en", "items", 2, { "items.other": "many" }, {}),
    "items.other",
  );
  assert.equal(
    pluralKey("pl", "items", 2, { "items.other": "many" }, {}),
    "items.other",
  );
});

test("localeDirection identifies left-to-right and right-to-left locales", () => {
  assert.equal(localeDirection("en-US"), "ltr");
  assert.equal(localeDirection("ar"), "rtl");
});
