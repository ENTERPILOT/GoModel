import test from "node:test";
import assert from "node:assert/strict";

import {
  EMBEDDING_PREVIEW_VALUES,
  MAX_IMAGE_COUNT,
  PLAYGROUND_MODES,
  abbreviateEmbeddingResponse,
  abbreviateImageResponse,
  buildEmbeddingRequest,
  buildImageRequest,
  buildSpeechRequest,
  embeddingDimensionsValid,
  embeddingInputs,
  embeddingSummary,
  imageResults,
  mediaModeById,
  modeModelOptions,
  normalizeMode,
  transcriptionFields,
  transcriptionPreview,
  transcriptionText,
} from "../src/pages/playground/playgroundMedia.js";

test("media modes map to the gateway's public paths", () => {
  assert.deepEqual(PLAYGROUND_MODES, ["chat", "image", "speech", "transcription", "embeddings"]);
  assert.equal(mediaModeById("image").path, "/v1/images/generations");
  assert.equal(mediaModeById("speech").path, "/v1/audio/speech");
  assert.equal(mediaModeById("transcription").path, "/v1/audio/transcriptions");
  assert.equal(mediaModeById("embeddings").path, "/v1/embeddings");
  assert.equal(mediaModeById("chat"), null);
  assert.equal(normalizeMode("speech"), "speech");
  assert.equal(normalizeMode("video"), "chat");
  assert.equal(normalizeMode(""), "chat");
});

test("the model picker lists models advertising the mode first, then unannotated ones", () => {
  const inventory = [
    { selector: "gpt-4o", provider_name: "openai", model: { id: "gpt-4o", metadata: { modes: ["chat"] } } },
    { selector: "gpt-image-1", provider_name: "openai", model: { id: "gpt-image-1", metadata: { modes: ["image_generation", "image_edit"] } } },
    { selector: "tts-1", provider_name: "openai", model: { id: "tts-1", metadata: { modes: ["audio_speech"] } } },
    { selector: "whisper-1", provider_name: "openai", model: { id: "whisper-1", metadata: { modes: ["audio_transcription"] } } },
    { selector: "text-embedding-3-small", provider_name: "openai", model: { id: "text-embedding-3-small", metadata: { modes: ["embedding"] } } },
    { selector: "dall-e-2", provider_name: "openai", model: { id: "dall-e-2" } },
    { selector: "local", provider_name: "ollama", model: { id: "local" } },
  ];
  const ids = (mode) => modeModelOptions(inventory, mode).map((o) => o.id);
  assert.deepEqual(ids("chat"), ["dall-e-2", "gpt-4o", "local"]);
  assert.deepEqual(ids("image"), ["gpt-image-1", "dall-e-2", "local"]);
  assert.deepEqual(ids("speech"), ["tts-1", "dall-e-2", "local"]);
  assert.deepEqual(ids("transcription"), ["whisper-1", "dall-e-2", "local"]);
  assert.deepEqual(ids("embeddings"), ["text-embedding-3-small", "dall-e-2", "local"]);
  assert.deepEqual(modeModelOptions(undefined, "image"), []);
});

test("buildImageRequest sends n and size only when they differ from the defaults", () => {
  assert.deepEqual(buildImageRequest({ model: " dall-e-3 ", prompt: "a cat", size: "", n: 1 }), {
    model: "dall-e-3",
    prompt: "a cat",
  });
  assert.deepEqual(buildImageRequest({ model: "m", prompt: "p", size: "1024x1024", n: "3" }), {
    model: "m",
    prompt: "p",
    n: 3,
    size: "1024x1024",
  });
  assert.equal(buildImageRequest({ model: "m", prompt: "p", n: 99 }).n, MAX_IMAGE_COUNT);
  assert.equal(buildImageRequest({ model: "m", prompt: "p", size: "huge" }).size, undefined);
  assert.equal(buildImageRequest({ model: "m", prompt: "p", n: 1.5 }).n, undefined);
});

test("buildSpeechRequest defaults the voice and omits the mp3 default format", () => {
  assert.deepEqual(buildSpeechRequest({ model: "tts-1", input: "Hi", voice: "", format: "mp3" }), {
    model: "tts-1",
    input: "Hi",
    voice: "alloy",
  });
  assert.deepEqual(buildSpeechRequest({ model: "tts-1", input: "Hi", voice: " nova ", format: "wav" }), {
    model: "tts-1",
    input: "Hi",
    voice: "nova",
    response_format: "wav",
  });
  assert.equal(buildSpeechRequest({ model: "m", input: "x", format: "pcm" }).response_format, undefined);
});

test("transcription fields drop blank options and the preview describes the file", () => {
  const fields = transcriptionFields({ model: "whisper-1", language: " en ", prompt: "  " });
  assert.deepEqual(fields, [
    ["model", "whisper-1"],
    ["language", "en"],
  ]);
  assert.deepEqual(transcriptionPreview(fields, { name: "a.mp3", type: "audio/mpeg", size: 42 }), {
    model: "whisper-1",
    language: "en",
    file: { name: "a.mp3", type: "audio/mpeg", size: 42 },
  });
  assert.equal(transcriptionPreview(fields, null).file, null);
});

test("imageResults renders URLs and base64 images typed by output_format", () => {
  assert.deepEqual(
    imageResults({
      output_format: "webp",
      data: [
        { b64_json: "AAAA" },
        { url: "https://example.com/a.png", revised_prompt: "a tabby cat" },
        { revised_prompt: "nothing" },
        null,
      ],
    }),
    [
      { src: "data:image/webp;base64,AAAA", revisedPrompt: "" },
      { src: "https://example.com/a.png", revisedPrompt: "a tabby cat" },
    ],
  );
  assert.equal(imageResults({ data: [{ b64_json: "AA" }] })[0].src, "data:image/png;base64,AA");
  assert.deepEqual(imageResults(null), []);
});

test("abbreviateImageResponse stubs long base64 payloads only", () => {
  const long = "A".repeat(100);
  const body = { created: 1, data: [{ b64_json: long }, { b64_json: "short" }, { url: "u" }] };
  const shown = abbreviateImageResponse(body);
  assert.equal(shown.created, 1);
  assert.equal(shown.data[0].b64_json, "A".repeat(32) + "… (100 chars)");
  assert.equal(shown.data[1].b64_json, "short");
  assert.deepEqual(shown.data[2], { url: "u" });
  assert.equal(body.data[0].b64_json, long, "the original body is untouched");
  assert.equal(abbreviateImageResponse(null), null);
});

test("transcriptionText reads JSON and plain-text responses", () => {
  assert.equal(transcriptionText({ text: "hello" }), "hello");
  assert.equal(transcriptionText("WEBVTT\n\nhello"), "WEBVTT\n\nhello");
  assert.equal(transcriptionText({}), "");
  assert.equal(transcriptionText(null), "");
});

test("embeddingInputs takes one trimmed text per non-blank line", () => {
  assert.deepEqual(embeddingInputs(" first \n\n  \nsecond line\r\n"), ["first", "second line"]);
  assert.deepEqual(embeddingInputs("héllo 世界 🙂"), ["héllo 世界 🙂"]);
  assert.deepEqual(embeddingInputs(""), []);
  assert.deepEqual(embeddingInputs(undefined), []);
});

test("buildEmbeddingRequest sends one text as a string and several as an array", () => {
  assert.deepEqual(buildEmbeddingRequest({ model: " text-embedding-3-small ", input: "hello\n" }), {
    model: "text-embedding-3-small",
    input: "hello",
  });
  assert.deepEqual(buildEmbeddingRequest({ model: "m", input: "a\nb" }).input, ["a", "b"]);
  assert.deepEqual(buildEmbeddingRequest({ model: "m", input: "" }).input, []);
});

test("buildEmbeddingRequest sends encoding_format and dimensions only when set", () => {
  assert.deepEqual(buildEmbeddingRequest({ model: "m", input: "a", format: "float", dimensions: "" }), {
    model: "m",
    input: "a",
  });
  assert.deepEqual(buildEmbeddingRequest({ model: "m", input: "a", format: "base64", dimensions: 256 }), {
    model: "m",
    input: "a",
    encoding_format: "base64",
    dimensions: 256,
  });
  assert.equal(buildEmbeddingRequest({ model: "m", input: "a", dimensions: "512" }).dimensions, 512);
  for (const bad of [0, -3, 1.5, "abc", null, undefined, Infinity]) {
    assert.equal(buildEmbeddingRequest({ model: "m", input: "a", dimensions: bad }).dimensions, undefined);
  }
  assert.equal(buildEmbeddingRequest({ model: "m", input: "a", format: "int8" }).encoding_format, undefined);
});

test("embeddingSummary counts vectors, their dimensions and prompt tokens", () => {
  const summary = embeddingSummary({
    data: [
      { object: "embedding", index: 0, embedding: Array.from({ length: 12 }, (_, i) => i / 10) },
      { object: "embedding", index: 1, embedding: Array.from({ length: 12 }, () => 0) },
    ],
    usage: { prompt_tokens: 7, total_tokens: 7 },
  });
  assert.equal(summary.count, 2);
  assert.equal(summary.dimensions, 12);
  assert.equal(summary.promptTokens, 7);
  assert.equal(summary.vectors[0].preview.length, EMBEDDING_PREVIEW_VALUES);
  assert.deepEqual(summary.vectors.map((v) => v.index), [0, 1]);
});

test("embeddingSummary reads base64 vector sizes and tolerates odd payloads", () => {
  // Four float32 values are 16 bytes, 24 base64 characters with padding.
  const b64 = Buffer.from(new Float32Array([1, 2, 3, 4]).buffer).toString("base64");
  const summary = embeddingSummary({ data: [{ index: 0, embedding: b64 }] });
  assert.equal(summary.dimensions, 4);
  assert.equal(summary.vectors[0].base64, true);
  assert.equal(summary.vectors[0].preview, null);
  assert.equal(summary.promptTokens, null);

  const mixed = embeddingSummary({ data: [{ embedding: [1, 2] }, null, { embedding: [1, 2, 3] }] });
  assert.equal(mixed.count, 2);
  assert.equal(mixed.dimensions, null);
  assert.deepEqual(mixed.vectors.map((v) => v.index), [0, 1]);

  assert.equal(embeddingSummary(null).count, 0);
  assert.equal(embeddingSummary({ data: "nope" }).count, 0);
});

test("abbreviateEmbeddingResponse trims long vectors and leaves the rest alone", () => {
  const long = Array.from({ length: 1536 }, (_, i) => i);
  const body = {
    object: "list",
    data: [
      { index: 0, embedding: long },
      { index: 1, embedding: [0.1, 0.2] },
      { index: 2, embedding: "A".repeat(100) },
    ],
    usage: { prompt_tokens: 3 },
  };
  const short = abbreviateEmbeddingResponse(body);
  assert.deepEqual(short.data[0].embedding, [...long.slice(0, EMBEDDING_PREVIEW_VALUES), "… (1536 values)"]);
  assert.deepEqual(short.data[1].embedding, [0.1, 0.2]);
  assert.equal(short.data[2].embedding, "A".repeat(32) + "… (100 chars)");
  assert.deepEqual(short.usage, { prompt_tokens: 3 });
  assert.equal(body.data[0].embedding.length, 1536, "the original response is not changed");
  assert.equal(abbreviateEmbeddingResponse(null), null);
  assert.deepEqual(abbreviateEmbeddingResponse({ error: "x" }), { error: "x" });
});

test("embeddingSummary reads per-token embedding matrices", () => {
  const row = (n) => Array.from({ length: 1024 }, (_, i) => (i + n) / 1000);
  const summary = embeddingSummary({
    data: [
      { index: 0, embedding: [row(1), row(2), row(3)] },
      { index: 1, embedding: [row(4)] },
    ],
    usage: { prompt_tokens: 4 },
  });
  assert.equal(summary.count, 2);
  assert.equal(summary.dimensions, 1024);
  assert.equal(summary.promptTokens, 4);
  assert.deepEqual(summary.vectors.map((v) => v.rows), [3, 1]);
  assert.deepEqual(summary.vectors[0].preview, row(1).slice(0, EMBEDDING_PREVIEW_VALUES));

  const flat = embeddingSummary({ data: [{ embedding: [1, 2] }] });
  assert.equal(flat.vectors[0].rows, null);

  const mixed = embeddingSummary({ data: [{ embedding: [1, 2, 3] }, { embedding: [[1, 2], [3, 4]] }] });
  assert.equal(mixed.count, 2);
  assert.equal(mixed.dimensions, null);
  assert.deepEqual(mixed.vectors.map((v) => v.length), [3, 2]);
});

test("embeddingSummary skips matrices that are not per-token float vectors", () => {
  for (const bad of [
    [[]],
    [[1, 2], []],
    [[1, 2], [3]],
    [[1, "2"]],
    [[1, NaN]],
    [[1, Infinity]],
    [[1, 2], 3],
    [1, [2]],
    [[[1, 2]]],
    [null],
  ]) {
    assert.equal(embeddingSummary({ data: [{ embedding: bad }] }).count, 0, JSON.stringify(bad));
  }
});

test("abbreviateEmbeddingResponse trims per-token matrices to their first row", () => {
  const row = (n) => Array.from({ length: 1024 }, (_, i) => i + n);
  const body = {
    data: [
      { index: 0, embedding: [row(0), row(1), row(2)] },
      { index: 1, embedding: [row(3)] },
      { index: 2, embedding: [[0.1, 0.2], [0.3, 0.4]] },
      { index: 3, embedding: [[0.5, 0.6]] },
    ],
  };
  const short = abbreviateEmbeddingResponse(body);
  const first = [...row(0).slice(0, EMBEDDING_PREVIEW_VALUES), "… (1024 values)"];
  assert.deepEqual(short.data[0].embedding, [first, "… (3 × 1024 values)"]);
  assert.deepEqual(short.data[1].embedding, [[...row(3).slice(0, EMBEDDING_PREVIEW_VALUES), "… (1024 values)"]]);
  assert.deepEqual(short.data[2].embedding, [[0.1, 0.2], "… (2 × 2 values)"]);
  assert.deepEqual(short.data[3].embedding, [[0.5, 0.6]]);
  assert.equal(body.data[0].embedding.length, 3, "the original response is not changed");
  assert.ok(JSON.stringify(short).length < 1000);
});

test("embeddingDimensionsValid accepts a blank field or a positive integer only", () => {
  for (const ok of ["", "  ", null, undefined, 1, 256, "1536", " 512 ", "9007199254740991"]) {
    assert.equal(embeddingDimensionsValid(ok), true, String(ok));
  }
  for (const bad of [0, -3, 1.5, "abc", "1e400", "1e300", "9007199254740993", Infinity, NaN,
    "256.0000000000000001", "1.0000000000000001", "9007199254740991.4", "0x100", "0b11", "1e3", "+5", "-0", "256.0"]) {
    assert.equal(embeddingDimensionsValid(bad), false, String(bad));
  }
});

test("embeddingSummary skips items without a vector", () => {
  for (const item of [
    {},
    { embedding: null },
    { embedding: 42 },
    { embedding: { a: 1 } },
    { embedding: [null, "x"] },
    { embedding: [1, "2"] },
    { embedding: [1, NaN] },
    { embedding: [1, Infinity] },
    { embedding: [true, false] },
    { embedding: [] },
    { embedding: "" },
    { embedding: "   " },
  ]) {
    const summary = embeddingSummary({ data: [item] });
    assert.equal(summary.count, 0, JSON.stringify(item));
    assert.equal(summary.dimensions, null);
  }
  const kept = embeddingSummary({ data: [{}, { index: 3, embedding: [1, 2] }] });
  assert.equal(kept.count, 1);
  assert.equal(kept.vectors[0].index, 3);
});

test("embeddingSummary gives no size for a string that is not whole base64 float32s", () => {
  for (const bad of ["not base64!!", "AAA=", "AAAAA", "AAAA====", "A@AA"]) {
    const summary = embeddingSummary({ data: [{ embedding: bad }] });
    assert.equal(summary.count, 1, bad);
    assert.equal(summary.vectors[0].length, null, bad);
    assert.equal(summary.dimensions, null, bad);
  }
  // Base64 with line breaks still decodes.
  const b64 = Buffer.from(new Float32Array([1, 2, 3, 4, 5, 6]).buffer).toString("base64");
  const wrapped = b64.slice(0, 10) + "\n" + b64.slice(10);
  assert.equal(embeddingSummary({ data: [{ embedding: wrapped }] }).dimensions, 6);
  // One unknown size means no shared dimension is claimed.
  const mixed = embeddingSummary({ data: [{ embedding: b64 }, { embedding: "AAA=" }] });
  assert.equal(mixed.dimensions, null);
  assert.deepEqual(mixed.vectors.map((v) => v.length), [6, null]);
});

test("buildEmbeddingRequest never sends a rounded dimensions value", () => {
  assert.equal(buildEmbeddingRequest({ model: "m", input: "a", dimensions: "9007199254740993" }).dimensions, undefined);
  assert.equal(buildEmbeddingRequest({ model: "m", input: "a", dimensions: "9007199254740991" }).dimensions, 9007199254740991);
  for (const typed of ["256.0000000000000001", "0x100", "1e3"]) {
    assert.equal(buildEmbeddingRequest({ model: "m", input: "a", dimensions: typed }).dimensions, undefined, typed);
  }
  assert.equal(buildEmbeddingRequest({ model: "m", input: "a", dimensions: " 0256 " }).dimensions, 256);
});
