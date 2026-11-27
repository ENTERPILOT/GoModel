// Guard test for the two scales DESIGN.md §5 fixes (P3).
//
// P3 put every box on one of four radius tokens and every spacing value on
// a 4px grid. Both are invisible in review — a stray 6px gap or a 12px
// corner looks like a deliberate choice in a diff, and the page still
// renders. This test makes the scales mechanical instead:
//
//   - every border-radius is one of the four tokens (control / panel / pill
//     / circle) or a literal 0 / inherit, never a one-off px value;
//   - every padding / margin / gap length is a multiple of 4, except the
//     1px and 2px optical nudges (icon centring, border overlap);
//   - the --space-* scale and the four radius tokens keep their measured
//     values.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const SRC = join(ROOT, "src");
const THEMES_PATH = join(ROOT, "src/styles/themes.css");
const BASE_PATH = join(ROOT, "src/styles/base.css");

const RADIUS_TOKENS = [
  "var(--radius-control)",
  "var(--radius-panel)",
  "var(--radius-pill)",
  "var(--radius-circle)",
];

// The radius Arco itself ships, measured from the compiled
// @arco-design/web-react@2.66.0 stylesheet: --border-radius-small 2px,
// --border-radius-medium 4px, --border-radius-circle 50%. Arco also
// defines --border-radius-large 8px but never uses it in a component, so
// the app does not need that tier.
const RADIUS_VALUES = new Map([
  ["--radius-control", "2px"],
  ["--radius-panel", "4px"],
  ["--radius-pill", "999px"],
  ["--radius-circle", "50%"],
]);

// 1px and 2px are optical nudges, not spacing: they centre an icon inside
// a taller box or pull a border back under its neighbour. They stay.
const OPTICAL = new Set([1, 2]);

// The two values that stay off the grid on purpose: they are Arco's own
// component metrics, measured from @arco-design/web-react@2.66.0
// (`.arco-btn-size-default { padding: 0 15px }` and
// `.arco-table-td { padding: 9px 16px }`). The grid governs the app's own
// layout spacing; it does not re-cut the component specs the whole design
// system is aligned to (DESIGN.md §7 #12). Each entry names the file, the
// rule and the value, so nothing else can slip through.
const GRID_EXCEPTIONS = [
  {
    file: "src/styles/buttons.css",
    selector: ".btn",
    property: "padding",
    value: "0 15px",
  },
  {
    file: "src/styles/tables.css",
    selector: ".data-table td",
    property: "padding",
    value: "9px var(--space-cell)",
  },
];

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else if (full.endsWith(".css") || full.endsWith(".svelte")) out.push(full);
  }
  return out;
}

/** The CSS of a file: a .css file whole, a .svelte file's <style> blocks. */
function cssOf(path) {
  const text = readFileSync(path, "utf8");
  if (!path.endsWith(".svelte")) return text;
  const blocks = [];
  const re = /<style[^>]*>([\s\S]*?)<\/style>/g;
  let m;
  while ((m = re.exec(text))) blocks.push(m[1]);
  return blocks.join("\n");
}

function stripComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

const RADIUS_DECL = /(?<![\w-])border-radius\s*:\s*([^;{}]+)/g;
const SPACE_DECL =
  /(?<![\w-])((?:padding|margin|gap)(?:-(?:top|right|bottom|left))?)\s*:\s*([^;{}]+)/g;

/** The selector of the rule that owns a declaration, and the file it is in. */
function rules(css) {
  const stack = [];
  const out = [];
  for (const line of css.split("\n")) {
    const s = line.trim();
    if (s.endsWith("{") && s.includes("{")) {
      stack.push(s.startsWith("@") ? null : s.slice(0, -1).trim());
    } else if (s === "}" && stack.length) {
      stack.pop();
    }
    out.push({
      selector: [...stack].reverse().find((x) => x !== null) ?? "",
      line,
    });
  }
  return out;
}

const files = walk(SRC);

test("every border-radius is one of the four radius tokens", () => {
  const offenders = [];
  for (const file of files) {
    const css = stripComments(cssOf(file));
    for (const m of css.matchAll(RADIUS_DECL)) {
      const parts = m[1].trim().split(/\s+/);
      const ok = parts.every(
        (p) => RADIUS_TOKENS.includes(p) || p === "0" || p === "inherit",
      );
      if (!ok) {
        offenders.push(`${relative(ROOT, file)}: border-radius: ${m[1].trim()}`);
      }
    }
  }
  assert.deepEqual(
    offenders,
    [],
    "border-radius must use --radius-control / --radius-panel / --radius-pill / --radius-circle",
  );
});

test("every spacing length sits on the 4px grid", () => {
  const offenders = [];
  for (const file of files) {
    const rel = relative(ROOT, file);
    const css = stripComments(cssOf(file));
    for (const { selector, line } of rules(css)) {
      for (const m of line.matchAll(SPACE_DECL)) {
        const property = m[1].trim();
        const value = m[2].trim();
        const allowed = GRID_EXCEPTIONS.some(
          (e) =>
            e.file === rel &&
            e.selector === selector &&
            e.property === property &&
            e.value === value,
        );
        if (allowed) continue;
        for (const num of value.matchAll(/(-?\d*\.?\d+)px/g)) {
          const length = Math.abs(Number(num[1]));
          if (length === 0 || OPTICAL.has(length)) continue;
          if (length % 4 !== 0) {
            offenders.push(`${rel}: ${selector} { ${property}: ${value} }`);
          }
        }
      }
    }
  }
  assert.deepEqual(
    offenders,
    [],
    "padding / margin / gap must use multiples of 4 (1px and 2px nudges excepted)",
  );
});

test("the --space-* scale stays on the grid", () => {
  const css = stripComments(readFileSync(BASE_PATH, "utf8"));
  const seen = [];
  for (const m of css.matchAll(/--space-[\w-]+:\s*(\d+)px/g)) {
    seen.push(m[1]);
    assert.equal(
      Number(m[1]) % 4,
      0,
      `--space-* value ${m[1]}px is not a multiple of 4`,
    );
  }
  assert.ok(seen.length >= 6, "base.css should still define the space scale");
});

test("the radius tokens keep their measured Arco values", () => {
  const css = stripComments(readFileSync(THEMES_PATH, "utf8"));
  for (const [token, value] of RADIUS_VALUES) {
    const m = new RegExp(`${token}:\\s*([^;]+);`).exec(css);
    assert.ok(m, `themes.css is missing ${token}`);
    assert.equal(m[1].trim(), value, `${token} must stay ${value}`);
  }
});
