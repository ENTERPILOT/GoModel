// Guard for the Icon atom's contract: components import lucide icons by name
// and pass the icon itself to <Icon icon={...} />. A typo in that import is
// invisible to the toolchain — jsconfig runs with checkJs:false and the
// bundler does not warn on missing named exports — so it would ship as a
// silently blank SVG. This test resolves every lucide import in the source
// tree against the real package instead.

import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import * as lucide from "lucide";

const SRC = fileURLToPath(new URL("../src", import.meta.url));

function sourceFiles(dir) {
  return readdirSync(dir).flatMap((entry) => {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) return sourceFiles(full);
    return /\.(svelte|js)$/.test(entry) ? [full] : [];
  });
}

// { name: [file, ...] } for every `import { ... } from "lucide"` in the tree.
function importedIcons() {
  const found = new Map();
  for (const file of sourceFiles(SRC)) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(/import\s*\{([^}]*)\}\s*from\s*"lucide";/g)) {
      for (const raw of match[1].split(",")) {
        const name = raw.trim();
        if (!name) continue;
        if (!found.has(name)) found.set(name, []);
        found.get(name).push(file.slice(SRC.length + 1));
      }
    }
  }
  return found;
}

test("every lucide icon imported in the dashboard exists and is drawable", () => {
  const imported = importedIcons();
  assert.ok(imported.size > 0, "expected the dashboard to import lucide icons");

  for (const [name, files] of imported) {
    const icon = lucide[name];
    assert.ok(icon, `lucide has no export "${name}" (imported by ${files.join(", ")})`);
    // Icons are [tag, attrs, children?] node arrays; an empty one renders a
    // blank <svg>, which is the failure mode this guard exists to catch.
    assert.ok(
      Array.isArray(icon) && icon.length > 0,
      `lucide export "${name}" is not a drawable icon (imported by ${files.join(", ")})`,
    );
  }
});
