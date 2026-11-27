// Guard test for the design tokens in src/styles/themes.css.
//
// The dashboard's palette is the one place where a silent regression is
// invisible in review: a token value changes, every page still renders, and
// the text just becomes a little less readable. This test pins the token
// layer down:
//
//   - every semantic pair documented in DESIGN.md §6 stays at or above the
//     WCAG AA ratio (4.5:1) on the surface it is actually painted on;
//   - the six --chart-* tokens stay LITERAL colors, because chartTheme.js
//     reads them with getComputedStyle and hands the raw text to Chart.js —
//     a var() reference would reach the canvas as the string "var(--text)";
//   - the theme palette is defined exactly once (no prefers-color-scheme
//     duplicate that can drift out of sync).

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const THEMES_PATH = join(ROOT, "src/styles/themes.css");
const BUDGETS_PATH = join(ROOT, "src/styles/budgets.css");

const CSS = readFileSync(THEMES_PATH, "utf8");
// Strip comments BEFORE splitting declarations: a naive split(";") would
// glue a trailing /* ... */ onto the next declaration and swallow it.
const CSS_NO_COMMENTS = CSS.replace(/\/\*[\s\S]*?\*\//g, "");

const AA = 4.5;

/* ── parsing ─────────────────────────────────────────────────────────────── */

function blockBody(selector) {
  const at = CSS_NO_COMMENTS.indexOf(selector + " {");
  assert.notEqual(at, -1, `themes.css has no ${selector} block`);
  const open = CSS_NO_COMMENTS.indexOf("{", at);
  let depth = 0;
  for (let i = open; i < CSS_NO_COMMENTS.length; i++) {
    if (CSS_NO_COMMENTS[i] === "{") depth++;
    else if (CSS_NO_COMMENTS[i] === "}") {
      depth--;
      if (depth === 0) return CSS_NO_COMMENTS.slice(open + 1, i);
    }
  }
  throw new Error(`unbalanced braces in ${selector}`);
}

function declarations(body) {
  const map = new Map();
  for (const part of body.split(";")) {
    const colon = part.indexOf(":");
    if (colon === -1) continue;
    const name = part.slice(0, colon).trim();
    if (!name.startsWith("--")) continue;
    map.set(name, part.slice(colon + 1).trim());
  }
  return map;
}

const DARK = declarations(blockBody(":root"));
const LIGHT = declarations(blockBody('[data-theme="light"]'));

/** Resolve a token to its literal value, following var() aliases. */
function resolve(map, name) {
  const seen = new Set();
  let current = name;
  for (;;) {
    const raw = map.get(current);
    assert.notEqual(raw, undefined, `missing token ${current} in themes.css`);
    const alias = /^var\(\s*(--[\w-]+)\s*\)$/.exec(raw);
    if (!alias) return raw;
    assert.ok(!seen.has(current), `circular alias at ${current}`);
    seen.add(current);
    current = alias[1];
  }
}

/* ── color maths ─────────────────────────────────────────────────────────── */

function parseColor(expr) {
  const text = expr.trim();
  let m = /^#([0-9a-f]{6})$/i.exec(text);
  if (m) {
    const n = parseInt(m[1], 16);
    return { rgb: [(n >> 16) & 255, (n >> 8) & 255, n & 255], alpha: 1 };
  }
  m = /^#([0-9a-f]{3})$/i.exec(text);
  if (m) {
    const parts = m[1].split("");
    return {
      rgb: parts.map((p) => parseInt(p + p, 16)),
      alpha: 1,
    };
  }
  m = /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/i.exec(
    text,
  );
  if (m) {
    return {
      rgb: [Number(m[1]), Number(m[2]), Number(m[3])],
      alpha: m[4] === undefined ? 1 : Number(m[4]),
    };
  }
  return null;
}

function composite(color, backdrop) {
  if (color.alpha >= 1) return color.rgb;
  return color.rgb.map(
    (c, i) => c * color.alpha + backdrop[i] * (1 - color.alpha),
  );
}

function channel(value) {
  const c = value / 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}

function luminance(rgb) {
  return (
    0.2126 * channel(rgb[0]) +
    0.7152 * channel(rgb[1]) +
    0.0722 * channel(rgb[2])
  );
}

function contrast(a, b) {
  const l1 = luminance(a);
  const l2 = luminance(b);
  const hi = Math.max(l1, l2);
  const lo = Math.min(l1, l2);
  return (hi + 0.05) / (lo + 0.05);
}

/** Contrast of a foreground token painted on a background token. */
function ratio(map, fgName, bgName) {
  const fg = parseColor(resolve(map, fgName));
  const bg = parseColor(resolve(map, bgName));
  assert.ok(fg, `${fgName} does not resolve to a color: ${resolve(map, fgName)}`);
  assert.ok(bg, `${bgName} does not resolve to a color: ${resolve(map, bgName)}`);
  // A translucent background has to be composited over the surface it sits
  // on before the ratio means anything.
  const surface = parseColor(resolve(map, "--bg-surface")).rgb;
  const backdrop = composite(bg, surface);
  // ...and a translucent foreground over that backdrop.
  return contrast(composite(fg, backdrop), backdrop);
}

/* ── the documented pairs ─────────────────────────────────────────────────── */

const LIGHT_PAIRS = [
  ["--text", "--bg-surface"],
  ["--text", "--bg"],
  ["--text-muted", "--bg-surface"],
  ["--text-muted", "--bg"],
  ["--text-muted", "--bg-surface-hover"],
  ["--primary-text", "--bg-surface"],
  ["--primary-text", "--bg"],
  ["--accent", "--bg-surface"],
  ["--accent-strong", "--bg-surface"],
  ["--success-text", "--success-bg"],
  ["--warning-text", "--warning-bg"],
  ["--danger-text", "--danger-bg"],
  ["--on-fill", "--primary"],
  ["--on-fill", "--primary-hover"],
  ["--on-fill", "--primary-active"],
  ["--on-fill", "--danger"],
  ["--meter-label", "--cache-meter-uncached"],
  ["--category-1", "--bg"],
];

const DARK_PAIRS = [
  ["--text", "--bg-surface"],
  ["--text", "--bg"],
  ["--text-muted", "--bg-surface"],
  ["--text-muted", "--bg-surface-hover"],
  ["--primary-text", "--bg-surface"],
  ["--primary-text", "--bg"],
  ["--accent", "--bg-surface"],
  ["--accent-strong", "--bg-surface"],
  ["--success-text", "--success-bg"],
  ["--warning-text", "--warning-bg"],
  ["--danger-text", "--danger-bg"],
  ["--on-fill", "--primary"],
  ["--on-fill", "--primary-hover"],
  ["--on-fill", "--primary-active"],
  ["--on-fill", "--danger"],
  ["--meter-label", "--cache-meter-uncached"],
  ["--category-1", "--bg"],
];

function checkPairs(theme, pairs) {
  for (const [fg, bg] of pairs) {
    const value = ratio(theme, fg, bg);
    assert.ok(
      value >= AA,
      `${fg} on ${bg} is ${value.toFixed(2)}:1, below AA ${AA}:1`,
    );
  }
}

/* The focus indicator is a non-text UI element: WCAG 1.4.11 asks 3:1, not
   the 4.5:1 that text needs. Two rings, because a hue can never clear 3:1
   against its own ramp: --primary-ring sits on the page and the cards,
   --ring-on-fill sits on the filled buttons' own fill. */
const NON_TEXT = 3;
const RING_SURFACES = ["--bg", "--bg-surface", "--bg-surface-hover"];
const FILL_SURFACES = ["--primary", "--primary-hover", "--primary-active"];

function checkRing(theme, ringName, surfaces) {
  for (const surface of surfaces) {
    const value = ratio(theme, ringName, surface);
    assert.ok(
      value >= NON_TEXT,
      `${ringName} on ${surface} is ${value.toFixed(2)}:1, below the ${NON_TEXT}:1 focus-indicator floor`,
    );
  }
}

test("the focus rings clear the non-text contrast floor", () => {
  for (const theme of [LIGHT, DARK]) {
    checkRing(theme, "--primary-ring", RING_SURFACES);
    checkRing(theme, "--ring-on-fill", FILL_SURFACES);
  }
});

test("light theme pairs stay at or above WCAG AA", () => {
  checkPairs(LIGHT, LIGHT_PAIRS);
});

test("dark theme pairs stay at or above WCAG AA", () => {
  checkPairs(DARK, DARK_PAIRS);
});

test("the palette is defined exactly once per theme", () => {
  // A duplicated prefers-color-scheme block is how the two copies drifted
  // apart in the first place (audit findings #8 and #16).
  assert.equal(
    CSS_NO_COMMENTS.includes("prefers-color-scheme"),
    false,
    "themes.css must not carry a prefers-color-scheme block",
  );
  assert.equal(
    readFileSync(BUDGETS_PATH, "utf8").includes("prefers-color-scheme"),
    false,
    "budgets.css must not carry a prefers-color-scheme block",
  );
  // The light palette is selected by data-theme, which the store always
  // writes; there must be no second light palette hiding behind :root.
  assert.equal(
    CSS_NO_COMMENTS.match(/\[data-theme="light"\]/g).length,
    1,
    "themes.css must define the light palette exactly once",
  );
});

test("the tokens the audit found broken are defined", () => {
  for (const token of [
    "--accent-strong",
    "--category-1",
    "--border-strong",
    "--text-disabled",
    "--on-fill",
    "--meter-label",
    "--switch-thumb",
    "--primary-ring",
    "--ring-on-fill",
    "--primary-hover",
    "--primary-active",
  ]) {
    assert.ok(DARK.has(token), `themes.css is missing ${token}`);
    assert.ok(LIGHT.has(token), `themes.css is missing ${token}`);
  }
  // --accent-hover had three definitions and no consumer at all.
  assert.equal(DARK.has("--accent-hover"), false);
  assert.equal(LIGHT.has("--accent-hover"), false);
  // --info carried five unrelated meanings; it is now split and gone.
  assert.equal(DARK.has("--info"), false);
  assert.equal(LIGHT.has("--info"), false);
});

test("chart tokens stay literal colors", () => {
  const chartTokens = [
    "--chart-grid",
    "--chart-text",
    "--chart-day-marker",
    "--chart-tooltip-bg",
    "--chart-tooltip-border",
    "--chart-tooltip-text",
  ];
  for (const theme of [DARK, LIGHT]) {
    for (const token of chartTokens) {
      const raw = theme.get(token);
      assert.ok(raw, `themes.css is missing ${token}`);
      assert.equal(
        raw.includes("var("),
        false,
        `${token} must be a literal color, got ${raw}`,
      );
      assert.equal(
        raw.includes("color-mix("),
        false,
        `${token} must be a literal color, got ${raw}`,
      );
      assert.ok(
        parseColor(raw),
        `${token} is not a parseable color: ${raw}`,
      );
    }
  }
});
