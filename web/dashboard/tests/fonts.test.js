// Guard test for the typography rules DESIGN.md §5 fixes (P4).
//
// Arco's compiled stylesheet contains exactly one text-transform —
// `none` on `button, select` — so the app must not uppercase anything
// either: a diff cannot tell a deliberate `text-transform: uppercase`
// from a leftover, and CSS cannot be seen in the browser test suite.
//
//   - no rule may declare text-transform (except the explicit `none`);
//   - the font tokens exist and keep Arco's measured stacks;
//   - every font-family in the app resolves through a token, except the
//     two Canvas font strings that cannot read var() (chartTheme.js,
//     liveTokensLogic.js) — those must match --font-mono.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const SRC = join(ROOT, "src");
const THEMES_PATH = join(ROOT, "src/styles/themes.css");

// Arco's body stack, measured from the compiled
// @arco-design/web-react@2.66.0 stylesheet, and the app's mono stack.
// Quoted family names are equivalent to Arco's unquoted ones.
const FONT_SANS =
  'Inter, -apple-system, BlinkMacSystemFont, "PingFang SC", "Hiragino Sans GB", "Noto Sans", "Microsoft YaHei", "Helvetica Neue", Helvetica, Arial, sans-serif';
const FONT_MONO = '"SF Mono", Menlo, Consolas, monospace';

function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, "");
}

function walk(dir, fn) {
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) walk(p, fn);
    else fn(p);
  }
}

function styleBlocks(text) {
  const blocks = [];
  const re = /<style[^>]*>([\s\S]*?)<\/style>/g;
  let m;
  while ((m = re.exec(text))) blocks.push(m[1]);
  return blocks;
}

function normalizeStack(value) {
  // Collapse the token's pretty-printed newlines so formatting differences
  // don't fail the comparison — family order is what matters.
  return value.replaceAll(/\s+/g, " ").trim();
}

function declarations(css) {
  // "property: value;" pairs, tolerant of nesting (declarations keep their
  // full value text — parsing blocks is not needed for the assertions).
  return [...css.matchAll(/([a-z-]+)\s*:\s*([^;{}]+)[;{]/g)].map(
    (m) => [m[1], normalizeStack(m[2])],
  );
}

function fileFonts(path, text) {
  if (path.endsWith(".css")) return [text];
  if (path.endsWith(".svelte")) return styleBlocks(text);
  return [];
}

test("the font tokens exist and keep their measured Arco values", () => {
  const css = stripComments(readFileSync(THEMES_PATH, "utf-8"));
  const tokens = new Map(
    declarations(css)
      .filter(([p]) => p === "--font-sans" || p === "--font-mono")
      .map(([p, v]) => [p, v]),
  );
  assert.equal(
    tokens.get("--font-sans"),
    FONT_SANS,
    "--font-sans must stay Arco's body stack (CJK families included)",
  );
  assert.equal(
    tokens.get("--font-mono"),
    FONT_MONO,
    "--font-mono must stay the UI-first mono stack",
  );
});

test("no rule transforms text to uppercase", () => {
  walk(SRC, (path) => {
    if (!path.endsWith(".css") && !path.endsWith(".svelte")) return;
    const css = stripComments(readFileSync(path, "utf-8"));
    const text = path.endsWith(".svelte") ? styleBlocks(css).join("\n") : css;
    const found = [...text.matchAll(/text-transform\s*:\s*([^;}]+)/g)].map(
      (m) => m[1].trim(),
    );
    const offenders = found.filter((v) => v !== "none");
    assert.deepEqual(
      offenders,
      [],
      `${path} declares text-transform: ${offenders.join(", ")}`,
    );
  });
});

test("every font-family goes through a token or inherits", () => {
  walk(SRC, (path) => {
    if (!path.endsWith(".css") && !path.endsWith(".svelte")) return;
    for (const block of fileFonts(path, readFileSync(path, "utf-8"))) {
      for (const [prop, value] of declarations(stripComments(block))) {
        if (prop !== "font-family") continue;
        const ok =
          value === "var(--font-sans)" ||
          value === "var(--font-mono)" ||
          value === "inherit";
        assert.ok(
          ok,
          `${path}: font-family: ${value} must use var(--font-sans), var(--font-mono) or inherit`,
        );
      }
    }
  });
});

test("form controls inherit the font stack", () => {
  // The UA stylesheet gives buttons, inputs and selects their own default
  // font (Arial). Without an explicit inherit, every control that does not
  // set a font of its own renders in Arial instead of the app stack.
  const css = stripComments(
    readFileSync(join(ROOT, "src/styles/base.css"), "utf-8"),
  );
  const rule = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find(([, sel, body]) =>
    /(^|,)\s*(button|input|select|textarea)\b/.test(sel),
  );
  assert.ok(rule, "base.css must have a rule for form controls");
  const family = declarations(rule[2]).find(([p]) => p === "font-family");
  assert.ok(family, "the form-control rule must set font-family");
  assert.equal(
    family[1],
    "inherit",
    "form controls must inherit the font stack, not fall back to Arial",
  );
  // Every control type Arco resets has to be covered.
  for (const selector of ["button", "input", "optgroup", "select", "textarea"]) {
    assert.match(
      rule[1],
      new RegExp(`(^|,)\\s*${selector}\\b`),
      `the inherit rule must cover ${selector}`,
    );
  }
});

test("the Canvas font strings stay in sync with --font-mono", () => {
  // Canvas cannot resolve var(), so chartTheme.js and liveTokensLogic.js
  // spell the mono stack out. If --font-mono changes, these must follow.
  const canvasFont = "'SF Mono', Menlo, Consolas, monospace";
  const unquoted = FONT_MONO;
  const theme = stripComments(readFileSync(THEMES_PATH, "utf-8"));
  const mono = declarations(theme).find(([p]) => p === "--font-mono");
  assert.ok(mono, "--font-mono must be defined");
  assert.equal(
    mono[1].replaceAll('"', "'"),
    canvasFont,
    "chartTheme.js / liveTokensLogic.js canvas stacks must match --font-mono",
  );
  assert.ok(
    unquoted.replaceAll('"', "'") === canvasFont,
    "sanity: the measured mono stack is what the canvas uses",
  );
  for (const rel of [
    "src/lib/utils/chartTheme.js",
    "src/pages/overview/liveTokensLogic.js",
  ]) {
    const js = readFileSync(join(ROOT, rel), "utf-8");
    assert.ok(
      js.includes(canvasFont),
      `${rel} must spell the same mono stack as --font-mono`,
    );
  }
});
