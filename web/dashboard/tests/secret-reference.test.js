// The dashboard's copy of the gateway's secret-reference rule
// ($lib/utils/secretReference.js).

import test from "node:test";
import assert from "node:assert/strict";

import { hasSecretReference } from "../src/lib/utils/secretReference.js";

test("hasSecretReference matches the gateway's ${scheme:reference} rule", () => {
  assert.equal(hasSecretReference("${vault:prod/llm#openai}"), true);
  assert.equal(hasSecretReference("Bearer ${env:GITHUB_TOKEN}"), true);
  assert.equal(hasSecretReference("http://u:${file:/run/secrets/p}@proxy:3128"), true);

  assert.equal(hasSecretReference("sk-literal"), false);
  assert.equal(hasSecretReference("$${env:ESCAPED}"), false);
  assert.equal(hasSecretReference("${LEGACY}"), false);
  assert.equal(hasSecretReference("${VAR:-default}"), false);
  assert.equal(hasSecretReference("${Env:NAME}"), false);
  assert.equal(hasSecretReference(""), false);
  assert.equal(hasSecretReference(undefined), false);
});
