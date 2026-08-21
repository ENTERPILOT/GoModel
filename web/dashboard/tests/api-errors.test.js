import test from "node:test";
import assert from "node:assert/strict";

import {
  errorMessage,
  errorPayloadMessage,
  errorPayloadProvider,
  isGatewayAuthError,
} from "../src/lib/api/errors.js";

test("errorPayloadMessage extracts the admin error payload message", () => {
  assert.equal(
    errorPayloadMessage({ error: { message: "storage unavailable" } }, "fallback"),
    "storage unavailable",
  );
});

test("errorPayloadMessage falls back on any other shape", () => {
  for (const payload of [null, undefined, {}, "text", { error: "flat" }, { error: {} }]) {
    assert.equal(errorPayloadMessage(payload, "fallback"), "fallback");
  }
});

test("errorPayloadMessage trims, and treats blank as absent like errorMessage", () => {
  assert.equal(errorPayloadMessage({ error: { message: "  boom  " } }, "fallback"), "boom");
  assert.equal(errorPayloadMessage({ error: { message: "   " } }, "fallback"), "fallback");
  assert.equal(errorPayloadMessage({ error: { message: "" } }, "fallback"), "fallback");
});

test("errorMessage reads the result envelope, preferring message over error", () => {
  assert.equal(errorMessage({ data: { message: "  boom  " } }, "fallback"), "boom");
  assert.equal(errorMessage({ data: { error: "flat error" } }, "fallback"), "flat error");
  assert.equal(
    errorMessage({ data: { error: { message: "nested" } } }, "fallback"),
    "nested",
  );
  assert.equal(
    errorMessage({ data: { message: "wins", error: "loses" } }, "fallback"),
    "wins",
  );
});

test("errorMessage falls back when the envelope carries nothing usable", () => {
  for (const result of [null, {}, { data: null }, { data: {} }, { data: { message: "  " } }]) {
    assert.equal(errorMessage(result, "fallback"), "fallback");
  }
});

test("isGatewayAuthError separates gateway credential failures from provider 401s", () => {
  assert.equal(isGatewayAuthError({ error: { type: "authentication_error", message: "invalid API key" } }), true);
  assert.equal(isGatewayAuthError(null), true);
  assert.equal(isGatewayAuthError("not json"), true);
  assert.equal(
    isGatewayAuthError({ error: { type: "authentication_error", message: "Incorrect API key", provider: "openai" } }),
    false,
  );
  assert.equal(isGatewayAuthError({ error: { type: "rate_limit_error", message: "slow down" } }), false);
  assert.equal(errorPayloadProvider({ error: { provider: " anthropic " } }), "anthropic");
  assert.equal(errorPayloadProvider({ error: { message: "x" } }), "");
});
