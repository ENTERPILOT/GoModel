// Pure-logic tests for the provider docs URL helper.
import test from "node:test";
import assert from "node:assert/strict";

import {
  providerDocsUrl,
  PROVIDER_DOCS_BASE_URL,
  PROVIDER_DOCS_OVERVIEW_SLUG,
} from "../src/lib/utils/providerDocs.js";

const UTM = "utm_source=gomodel_dashboard";

test("providerDocsUrl normalizes the registry type", () => {
  // Underscores become dashes so opencode_go resolves to the opencode-go page.
  assert.equal(
    providerDocsUrl("opencode_go"),
    PROVIDER_DOCS_BASE_URL + "opencode-go?" + UTM,
  );
  // Whitespace and case are ignored.
  assert.equal(
    providerDocsUrl("  GEMINI  "),
    PROVIDER_DOCS_BASE_URL + "gemini?" + UTM,
  );
  // Ollama's docs page slug differs from the registry type.
  assert.equal(
    providerDocsUrl("ollama"),
    PROVIDER_DOCS_BASE_URL + "multiple-ollama?" + UTM,
  );
});

test("providerDocsUrl links every documented provider to its own page", () => {
  const documented = [
    "anthropic",
    "azure",
    "bailian",
    "bedrock",
    "bedrock-mantle",
    "chatgpt",
    "cohere",
    "deepseek",
    "elevenlabs",
    "gemini",
    "hetzner",
    "kimicode",
    "llamacpp",
    "llmd",
    "minimax",
    "opencode-go",
    "oracle",
    "sglang",
    "vertex",
    "vllm",
    "xai",
    "xiaomi",
  ];
  for (const slug of documented) {
    assert.equal(
      providerDocsUrl(slug),
      PROVIDER_DOCS_BASE_URL + slug + "?" + UTM,
      "expected " + slug + " to resolve to its own page",
    );
  }
});

test("providerDocsUrl falls back to overview for registered types without a page", () => {
  const fallback = [
    "openai",
    "openrouter",
    "chutes",
    "fireworks",
    "groq",
    "kilo",
    "meta",
    "zai",
  ];
  for (const type of fallback) {
    assert.equal(
      providerDocsUrl(type),
      PROVIDER_DOCS_BASE_URL + PROVIDER_DOCS_OVERVIEW_SLUG + "?" + UTM,
      "expected " + type + " to fall back to overview",
    );
  }
});

test("providerDocsUrl returns an empty string for blank input", () => {
  assert.equal(providerDocsUrl(""), "");
  assert.equal(providerDocsUrl("   "), "");
  assert.equal(providerDocsUrl(null), "");
  assert.equal(providerDocsUrl(undefined), "");
});

test("providerDocsUrl ignores inherited object keys on the override map", () => {
  // "constructor"/"toString" must fall back to overview, not read the
  // inherited property off PROVIDER_DOC_SLUG_OVERRIDES.
  for (const inherited of ["constructor", "toString", "__proto__"]) {
    assert.equal(
      providerDocsUrl(inherited),
      PROVIDER_DOCS_BASE_URL + PROVIDER_DOCS_OVERVIEW_SLUG + "?" + UTM,
      "expected " + inherited + " to fall back to overview",
    );
  }
});
