import test from "node:test";
import assert from "node:assert/strict";

import {
  renderAudioBody,
  renderImageBody,
} from "../src/pages/audit-logs/conversation-helpers.js";
import { loadMedia } from "../src/pages/audit-logs/media-loader.js";

test("renderAudioBody references stored audio by media id and keeps a hidden note", () => {
  const html = renderAudioBody({
    __audio__: true,
    content_type: "audio/mpeg",
    bytes: 4096,
    stored: true,
    media_id: "med_0123abcd",
  });
  assert.match(html, /<audio class="audit-audio-player" controls preload="none" data-media-id="med_0123abcd"><\/audio>/);
  assert.doesNotMatch(html, /src=/);
  assert.match(html, /<div class="audit-media-unavailable audit-audio-note" hidden>Media unavailable/);
  assert.match(html, /audio\/mpeg · 4\.0 KB/);
});

test("renderAudioBody still plays rows that carry inline base64", () => {
  const html = renderAudioBody({
    __audio__: true,
    content_type: "audio/wav",
    bytes: 3,
    stored: true,
    encoding: "base64",
    data: "aGk=",
  });
  assert.match(html, /src="data:audio\/wav;base64,aGk="/);
  assert.doesNotMatch(html, /data-media-id/);
});

test("renderAudioBody treats an unsafe media id as not stored", () => {
  const html = renderAudioBody({
    __audio__: true,
    content_type: "audio/mpeg",
    bytes: 3,
    stored: true,
    media_id: 'x" onerror="alert(1)',
  });
  assert.doesNotMatch(html, /data-media-id/);
  assert.doesNotMatch(html, /onerror/);
  assert.match(html, /LOGGING_LOG_AUDIO_BODIES=true/);
});

test("renderImageBody references stored images by media id", () => {
  const html = renderImageBody({
    __images__: true,
    images: [
      { role: "output", content_type: "image/png", bytes: 2048, stored: true, media_id: "med_ff00" },
    ],
    meta: { size: "1024x1024" },
  });
  assert.match(html, /<a data-media-id="med_ff00" target="_blank" rel="noopener">/);
  assert.match(html, /<img class="audit-image-preview" data-media-id="med_ff00" alt="Output" loading="lazy" \/>/);
  assert.match(html, /audit-media-unavailable/);
  assert.doesNotMatch(html, /src=/);
});

// fakeNode is the slice of Element the loader touches.
function fakeNode(tagName, id, parent) {
  const attrs = new Map([["data-media-id", id]]);
  return {
    tagName,
    parentElement: parent,
    getAttribute: (name) => (attrs.has(name) ? attrs.get(name) : null),
    setAttribute: (name, value) => attrs.set(name, value),
    removeAttribute: (name) => attrs.delete(name),
    attrs,
  };
}

function fakeContainer(children) {
  const note = { attrs: new Map([["hidden", ""]]), removeAttribute(name) { this.attrs.delete(name); } };
  const container = {
    tagName: "DIV",
    note,
    querySelector: (selector) => (selector === ".audit-media-unavailable" ? note : null),
    querySelectorAll: () => children,
  };
  for (const child of children) {
    if (!child.parentElement) child.parentElement = container;
  }
  return container;
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

test("loadMedia fetches each id once and hands elements object URLs", async () => {
  const calls = [];
  const audio = fakeNode("AUDIO", "med_a");
  const link = fakeNode("A", "med_b");
  const img = fakeNode("IMG", "med_b", link);
  const root = fakeContainer([audio, link, img]);
  const created = [];
  const revoked = [];

  const cleanup = loadMedia(root, {
    fetchMedia: async (id) => {
      calls.push(id);
      return { id };
    },
    createObjectURL: (blob) => {
      const url = "blob:" + blob.id;
      created.push(url);
      return url;
    },
    revokeObjectURL: (url) => revoked.push(url),
  });
  await flush();

  assert.deepEqual(calls, ["med_a", "med_b"], "shared ids download once");
  assert.equal(audio.attrs.get("src"), "blob:med_a");
  assert.equal(link.attrs.get("href"), "blob:med_b");
  assert.equal(img.attrs.get("src"), "blob:med_b");
  assert.equal(audio.attrs.get("data-media-state"), "loaded");

  cleanup();
  assert.deepEqual(revoked.sort(), created.sort(), "every object URL is revoked on cleanup");
});

test("loadMedia hides a failed element and reveals the unavailable note", async () => {
  const audio = fakeNode("AUDIO", "med_gone");
  const root = fakeContainer([audio]);

  loadMedia(root, {
    fetchMedia: async () => {
      throw new Error("404");
    },
    createObjectURL: () => "blob:never",
    revokeObjectURL: () => {},
  });
  await flush();

  assert.equal(audio.attrs.get("data-media-state"), "unavailable");
  assert.equal(audio.attrs.has("hidden"), true);
  assert.equal(root.note.attrs.has("hidden"), false, "the note is revealed");
});

test("loadMedia reveals the note through an image's wrapping link", async () => {
  const link = fakeNode("A", "med_gone");
  const img = fakeNode("IMG", "med_gone", link);
  const root = fakeContainer([link, img]);

  loadMedia(root, {
    fetchMedia: async () => {
      throw new Error("404");
    },
    createObjectURL: () => "blob:never",
    revokeObjectURL: () => {},
  });
  await flush();

  assert.equal(img.attrs.get("data-media-state"), "unavailable");
  assert.equal(root.note.attrs.has("hidden"), false);
});

test("loadMedia skips elements it already handled and tolerates a missing root", async () => {
  const audio = fakeNode("AUDIO", "med_a");
  audio.setAttribute("data-media-state", "loaded");
  const calls = [];
  loadMedia(fakeContainer([audio]), {
    fetchMedia: async (id) => {
      calls.push(id);
      return {};
    },
    createObjectURL: () => "blob:x",
    revokeObjectURL: () => {},
  });
  await flush();
  assert.deepEqual(calls, []);
  assert.equal(typeof loadMedia(null), "function");
  assert.equal(typeof loadMedia(fakeContainer([fakeNode("AUDIO", "med_x")])), "function", "no fetcher means no-op");
});

test("loadMedia revokes downloads that finish after cleanup", async () => {
  const audio = fakeNode("AUDIO", "med_slow");
  const revoked = [];
  let resolveBlob;
  const cleanup = loadMedia(fakeContainer([audio]), {
    fetchMedia: () => new Promise((resolve) => (resolveBlob = resolve)),
    createObjectURL: () => "blob:slow",
    revokeObjectURL: (url) => revoked.push(url),
  });
  cleanup();
  resolveBlob({});
  await flush();
  assert.equal(audio.attrs.has("src"), false, "a disposed pane must not receive a URL");
  assert.deepEqual(revoked, ["blob:slow"], "the late URL is released right away");
});
