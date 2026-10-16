import test from "node:test";
import assert from "node:assert/strict";

import {
  MAX_IMAGE_COUNT,
  PLAYGROUND_MODES,
  abbreviateImageResponse,
  buildImageRequest,
  buildSpeechRequest,
  imageResults,
  mediaModeById,
  modeModelOptions,
  normalizeMode,
  transcriptionFields,
  transcriptionPreview,
  transcriptionText,
} from "../src/pages/playground/playgroundMedia.js";

test("media modes map to the gateway's public paths", () => {
  assert.deepEqual(PLAYGROUND_MODES, ["chat", "image", "speech", "transcription"]);
  assert.equal(mediaModeById("image").path, "/v1/images/generations");
  assert.equal(mediaModeById("speech").path, "/v1/audio/speech");
  assert.equal(mediaModeById("transcription").path, "/v1/audio/transcriptions");
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
    { selector: "dall-e-2", provider_name: "openai", model: { id: "dall-e-2" } },
    { selector: "local", provider_name: "ollama", model: { id: "local" } },
  ];
  const ids = (mode) => modeModelOptions(inventory, mode).map((o) => o.id);
  assert.deepEqual(ids("chat"), ["dall-e-2", "gpt-4o", "local"]);
  assert.deepEqual(ids("image"), ["gpt-image-1", "dall-e-2", "local"]);
  assert.deepEqual(ids("speech"), ["tts-1", "dall-e-2", "local"]);
  assert.deepEqual(ids("transcription"), ["whisper-1", "dall-e-2", "local"]);
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
