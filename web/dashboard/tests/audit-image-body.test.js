import test from "node:test";
import assert from "node:assert/strict";

import {
  isImageBody,
  renderImageBody,
} from "../src/pages/audit-logs/conversation-helpers.js";

test("isImageBody detects the __images__ marker only", () => {
  assert.equal(isImageBody({ __images__: true, images: [] }), true);
  assert.equal(isImageBody({ __audio__: true }), false);
  assert.equal(isImageBody({ data: [{ b64_json: "aGk=" }] }), false);
  assert.equal(isImageBody(null), false);
});

test("renderImageBody shows stored images inline with role and size", () => {
  const html = renderImageBody({
    __images__: true,
    images: [
      {
        role: "output",
        content_type: "image/png",
        bytes: 2048,
        encoding: "base64",
        data: "aGVsbG8=",
        stored: true,
        revised_prompt: "a fluffy cat",
      },
    ],
    meta: { created: 1713833628, size: "1024x1024" },
  });
  assert.match(html, /<img class="audit-image-preview" src="data:image\/png;base64,aGVsbG8="/);
  assert.match(html, /Output · image\/png · 2\.0 KB/);
  assert.match(html, /a fluffy cat/);
  assert.match(html, /1024x1024/);
});

test("renderImageBody renders placeholders and hosted links without bytes", () => {
  const html = renderImageBody({
    __images__: true,
    images: [
      { role: "input", filename: "cat.png", content_type: "image/png", bytes: 857, stored: false },
      { role: "mask", filename: "mask.png", bytes: 10, stored: false, too_large: true },
      { role: "output", url: "https://img.example/1.png", stored: false },
    ],
    meta: { model: "gpt-image-1", prompt: "add <b>hat</b>" },
  });
  assert.doesNotMatch(html, /<img /);
  assert.match(html, /Input · cat\.png · image\/png · 857 B/);
  assert.match(html, /LOGGING_LOG_IMAGE_BODIES=true/);
  assert.match(html, /Image too large to store\./);
  assert.match(html, /<a class="audit-image-link mono" href="https:\/\/img\.example\/1\.png"/);
  assert.match(html, /add &lt;b&gt;hat&lt;\/b&gt;/);
});

test("renderImageBody never emits unsafe data or javascript URLs", () => {
  const html = renderImageBody({
    __images__: true,
    images: [
      { role: "output", content_type: "text/html", bytes: 3, encoding: "base64", data: "aGk=\"><ScRiPt>", stored: true },
      { role: "output", url: "JaVaScRiPt:alert(1)", stored: false },
    ],
  });
  assert.match(html, /src="data:image\/png;base64,aGk=ScRiPt"/);
  assert.doesNotMatch(html, /text\/html;base64/);
  // The renderer escapes everything it interpolates, so no tag of any casing
  // or spelling may survive beyond the fixed markup it emits itself.
  assert.doesNotMatch(html, /<\s*script/i);
  assert.doesNotMatch(html, /href\s*=\s*"\s*javascript:/i);
});

test("renderImageBody tolerates malformed image entries", () => {
  const html = renderImageBody({
    __images__: true,
    images: [null, 42, "junk", { role: "output", url: "https://img.example/ok.png" }],
    meta: { model: "gpt-image-1" },
  });
  assert.match(html, /https:\/\/img\.example\/ok\.png/);
  assert.doesNotMatch(html, /junk/);
  assert.match(html, /gpt-image-1/);
  assert.equal((html.match(/<figure/g) || []).length, 1);
});

test("renderImageBody with a non-array images field renders meta only", () => {
  const html = renderImageBody({ __images__: true, images: null, meta: { prompt: "p" } });
  assert.doesNotMatch(html, /<figure/);
  assert.match(html, /audit-audio-metadata/);
});
