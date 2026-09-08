// Guard for the editor modals' unsaved-changes contract: a dirty editor
// form must ask for confirmation before ANY close path discards it
// (Escape, backdrop click, header close button, Cancel), and a clean form
// must close without a prompt. There is no DOM test harness in this suite,
// so — like icons.test.js — this asserts the wiring contract directly on
// the component source.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SRC = fileURLToPath(new URL("../src", import.meta.url));
const editorDialog = readFileSync(
  join(SRC, "lib/components/organisms/EditorDialog.svelte"),
  "utf8",
);
const typedConfirm = readFileSync(
  join(SRC, "lib/components/organisms/TypedConfirmationDialog.svelte"),
  "utf8",
);
const confirmStore = readFileSync(
  join(SRC, "lib/stores/confirm.svelte.js"),
  "utf8",
);

test("EditorDialog marks the form dirty on user edits and resets on open", () => {
  // The form element itself funnels every field's input/change events into
  // the dirty flag, so each editor stays untouched.
  assert.match(editorDialog, /oninput=\{\(\) => \(dirty = true\)\}/);
  assert.match(editorDialog, /onchange=\{\(\) => \(dirty = true\)\}/);
  // Reopening a dialog starts from a clean slate; this also covers the
  // post-submit reopen, since every store closes the form after a
  // successful save.
  assert.match(editorDialog, /if \(open\) dirty = false;/);
  // While a dirty editor is open, reload/tab close triggers the browser's
  // native unsaved-changes prompt.
  assert.match(
    editorDialog,
    /if \(!open \|\| !dirty\) return;\s*\n\s*const onBeforeUnload = \(event\) => \{\s*\n\s*event\.preventDefault\(\);/,
  );
});

test("every EditorDialog close path goes through the discard confirmation", () => {
  // Modal (Escape + backdrop), the header close button, and Cancel must all
  // route through the same gate; no close path may call onclose directly.
  assert.match(
    editorDialog,
    /<Modal \{open\} variant="editor" onclose=\{requestClose\}>/,
  );
  assert.match(editorDialog, /onclick=\{requestClose\}/);
  assert.equal(
    editorDialog.match(/onclick=\{\(\) => onclose\?\.\(\)\}/g),
    null,
    "a close path bypasses requestClose",
  );
  // A clean form closes as before; a dirty form opens the shared
  // confirmation dialog instead of closing, stacked above the editor.
  assert.match(editorDialog, /if \(!dirty\) \{\s*\n\s*onclose\?\.\(\);/);
  assert.match(
    editorDialog,
    /confirmDialog\.open\(\{[\s\S]*?title: m\.editor_discard_title\(\)[\s\S]*?stacked: true,[\s\S]*?onConfirm: \(\) => \{[\s\S]*?onclose\?\.\(\);/,
  );
});

test("the confirmation dialog only asks for typed text when required", () => {
  // The discard prompt needs no typed confirmation: the input renders only
  // when requiredText is set, and the store's default requiredText is empty
  // (so ready() is immediately true for simple confirmations). A stacked
  // dialog lightens its backdrop and lifts its shell above the editor.
  assert.match(typedConfirm, /\{#if dialog\.requiredText\}/);
  assert.match(typedConfirm, /stacked=\{dialog\.stacked\}/);
  assert.match(confirmStore, /requiredText: "",/);
  assert.match(confirmStore, /stacked: false,/);
});

test("the discard prompt is translated in every locale", () => {
  const keys = [
    "editor_discard_title",
    "editor_discard_message",
    "editor_discard_confirm",
  ];
  for (const locale of ["en", "de", "pl", "zh-CN"]) {
    const catalog = JSON.parse(
      readFileSync(new URL(`../messages/${locale}.json`, import.meta.url), "utf8"),
    );
    for (const key of keys) {
      assert.ok(
        typeof catalog[key] === "string" && catalog[key].trim() !== "",
        `${locale}.json is missing a translation for ${key}`,
      );
    }
  }
});
