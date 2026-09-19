// Reset-button tests for the Providers Overview cards. The button's state
// logic is pure (providersLogic.js); the POST flow lives in the Svelte state
// module, which node cannot import (runes), so its wiring is asserted against
// the source the same way mcp-servers.test.js inspects fetchServers.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import {
  providerCircuitState,
  providerBreakerResettable,
} from "../src/pages/overview/providersLogic.js";

const CLIENT_SOURCE = fileURLToPath(
  new URL("../src/lib/api/client.js", import.meta.url),
);
const STATE_SOURCE = fileURLToPath(
  new URL("../src/pages/overview/overviewState.svelte.js", import.meta.url),
);

test("only an open or half-open breaker offers the reset button", () => {
  const withState = (state) => ({ name: "openai", circuit_state: state });

  // Tripped states: resettable.
  assert.equal(providerBreakerResettable(withState("open")), true);
  assert.equal(providerBreakerResettable(withState("half-open")), true);

  // Healthy, unknown, and never-served providers: greyed out.
  assert.equal(providerBreakerResettable(withState("closed")), false);
  assert.equal(providerBreakerResettable(withState("")), false);
  assert.equal(providerBreakerResettable(withState("tripped")), false);

  // Missing or malformed rows stay disabled rather than throwing.
  assert.equal(providerBreakerResettable({}), false);
  assert.equal(providerBreakerResettable({ circuit_state: null }), false);
  assert.equal(providerBreakerResettable(null), false);
  assert.equal(providerBreakerResettable(undefined), false);
});

test("providerCircuitState reads the top-level circuit_state field verbatim", () => {
  assert.equal(providerCircuitState({ circuit_state: "half-open" }), "half-open");
  assert.equal(providerCircuitState({ circuit_state: " open " }), "open");
  assert.equal(providerCircuitState({ circuit_state: "" }), "");
  assert.equal(providerCircuitState({}), "");
  assert.equal(providerCircuitState(null), "");
});

test("resetCircuitBreaker posts to the provider's circuit-breaker reset endpoint", () => {
  const source = readFileSync(CLIENT_SOURCE, "utf8");
  const declaration = source.match(
    /export function resetCircuitBreaker\(providerName\) \{[\s\S]*?\n\}/,
  );
  assert.ok(declaration, "resetCircuitBreaker declaration missing");

  const body = declaration[0];
  // The provider name is path-encoded, and the endpoint matches the gateway
  // route exactly; the request is a body-less POST.
  assert.match(body, /sendJSON\(/);
  assert.match(body, /encodeURIComponent\(providerName\)/);
  assert.match(body, /\/admin\/providers\/"\s*\+\s*\n?\s*encodeURIComponent\(providerName\)\s*\+\s*\n?\s*"\/circuit-breaker\/reset"/);
  assert.match(body, /"POST"/);
});

test("resetBreaker posts per provider name, flashes failures, and refreshes status", () => {
  const source = readFileSync(STATE_SOURCE, "utf8");
  const declaration = source.match(
    /async resetBreaker\(provider\) \{[\s\S]*?\n  \}/,
  );
  assert.ok(declaration, "resetBreaker declaration missing");
  const body = declaration[0];

  // The right provider name goes to the API client.
  assert.match(body, /const name = String\(\(provider && provider\.name\) \|\| ""\)\.trim\(\)/);
  assert.match(body, /resetCircuitBreaker\(name\)/);
  // A re-entrant click while a reset is in flight does nothing.
  assert.match(body, /this\.resettingName\)/);
  // 204 refreshes the provider status data through the existing fetch path,
  // so the button re-disables on the fresh circuit_state.
  assert.match(body, /await this\.fetch\(\)/);
  // Every failure path surfaces through the flash store; success flashes too.
  assert.match(body, /flash\.error\(m\.overview_reset_breaker_failed\(\)\)/);
  assert.match(body, /flash\.error\(m\.overview_reset_breaker_unavailable\(\)\)/);
  assert.match(body, /flash\.success\(m\.overview_reset_breaker_success\(\{ name \}\)\)/);
  // The in-flight marker clears even when the POST throws.
  assert.match(body, /finally \{\s*this\.resettingName = "";?\s*\}/);
});

test("the reset button renders on every card, disabled off the breaker state", () => {
  const source = readFileSync(
    fileURLToPath(
      new URL("../src/pages/overview/ProviderStatusCard.svelte", import.meta.url),
    ),
    "utf8",
  );

  assert.match(source, /providerBreakerResettable/);
  assert.match(
    source,
    /disabled=\{!breakerResettable \|\| resetting\}/,
  );
  assert.match(source, /providerStatusState\.resetBreaker\(provider\)/);
  // The reset guard is global: any in-flight reset disables ALL cards'
  // buttons (single-flight). It does not compare resettingName to a
  // specific provider.name.
  assert.match(source, /const resetting = \$derived\(providerStatusState\.resettingName\)/);
});
