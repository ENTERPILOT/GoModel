// Pure-logic tests for the Rate Limits page, ported from the legacy
// internal/admin/dashboard/static/js/modules/rate-limits.test.cjs.
// Store-level cases (fetch gating, 503 handling, submit move flow) exercise
// network wiring and are covered by the shared api client; the rule matching,
// payload building, and gauge derivation logic lives in rateLimitsLogic.js.

import test from "node:test";
import assert from "node:assert/strict";
import { overwriteGetLocale } from "../src/lib/paraglide/runtime.js";

import {
  defaultRateLimitForm,
  rateLimitScopeMeta,
  rateLimitSubjectFieldLabel,
  rateLimitSubjectPlaceholder,
  syncRateLimitScope,
  rateLimitPeriodSeconds,
  rateLimitPeriodFromSeconds,
  syncRateLimitPeriodSeconds,
  rateLimitKey,
  rateLimitSourceLabel,
  rateLimitIsReadOnly,
  rateLimitUsagePercent,
  filteredRateLimits,
  normalizeRateLimitListPayload,
  groupRateLimits,
  rateLimitNormalizedIdentity,
  rateLimitIdentityMoved,
  rateLimitFormPayload,
  rateLimitInspectorSections,
  rateLimitPressurePercent,
  rateLimitPressureStyle,
  rateLimitPressureClass,
  rateLimitGaugeClassForModel,
  rateLimitGaugeClassForProvider,
  rateLimitGaugeTitle,
  rateLimitInspectorSummary,
  formatRateLimitNumber,
} from "../src/pages/rate-limits/rateLimitsLogic.js";

test("rate-limit numbers follow the selected locale", () => {
  overwriteGetLocale(() => "pl");
  try {
    assert.equal(formatRateLimitNumber(1234567), "1 234 567");
    assert.equal(formatRateLimitNumber("invalid"), "0");
  } finally {
    overwriteGetLocale(() => "en");
  }
});

test("period helpers map names and seconds both ways", () => {
  assert.equal(rateLimitPeriodSeconds("minute"), 60);
  assert.equal(rateLimitPeriodSeconds("hour"), 3600);
  assert.equal(rateLimitPeriodSeconds("day"), 86400);
  assert.equal(rateLimitPeriodSeconds("concurrent"), 0);
  assert.equal(rateLimitPeriodSeconds("custom"), -1);

  assert.equal(rateLimitPeriodFromSeconds(60), "minute");
  assert.equal(rateLimitPeriodFromSeconds(0), "concurrent");
  assert.equal(rateLimitPeriodFromSeconds(7200), "custom");
});

test("syncRateLimitPeriodSeconds maps the period and clears hidden token limits", () => {
  const form = {
    scope: "user_path",
    subject: "/",
    period: "hour",
    period_seconds: 60,
    max_requests: "5",
    max_tokens: "1000",
  };
  syncRateLimitPeriodSeconds(form);
  assert.equal(form.period_seconds, 3600);
  assert.equal(form.max_tokens, "1000");

  // Switching to concurrent must zero the period and drop the now-hidden
  // token limit so it cannot invisibly block the save.
  form.period = "concurrent";
  syncRateLimitPeriodSeconds(form);
  assert.equal(form.period_seconds, 0);
  assert.equal(form.max_tokens, "");

  // Custom keeps whatever seconds are already set for the user to edit.
  form.period = "custom";
  form.period_seconds = 300;
  syncRateLimitPeriodSeconds(form);
  assert.equal(form.period_seconds, 300);
});

test("rateLimitFormPayload validates and builds the upsert payload", () => {
  const { payload, error } = rateLimitFormPayload({
    scope: "user_path",
    subject: " /team/alpha ",
    period: "minute",
    period_seconds: 60,
    max_requests: "100",
    max_tokens: "5000",
  });
  assert.equal(error, undefined);
  assert.deepEqual(payload, {
    scope: "user_path",
    subject: "/team/alpha",
    per_child: false,
    limit_key: { period_seconds: 60 },
    max_requests: 100,
    max_tokens: 5000,
  });

  assert.match(
    rateLimitFormPayload({
      scope: "user_path",
      subject: "/",
      period: "minute",
      period_seconds: 60,
      max_requests: "",
      max_tokens: "",
    }).error,
    /max requests, max tokens/i,
  );

  assert.match(
    rateLimitFormPayload({
      scope: "user_path",
      subject: "/",
      period: "concurrent",
      period_seconds: 0,
      max_requests: "5",
      max_tokens: "10",
    }).error,
    /concurrent/i,
  );

  assert.match(
    rateLimitFormPayload({
      scope: "user_path",
      subject: "/",
      period: "minute",
      period_seconds: 60,
      max_requests: "-3",
      max_tokens: "",
    }).error,
    /positive integer/i,
  );

  assert.match(
    rateLimitFormPayload({
      scope: "user_path",
      subject: "/",
      period: "custom",
      period_seconds: -5,
      max_requests: "5",
      max_tokens: "",
    }).error,
    /period seconds/i,
  );

  // Blank custom seconds must not coerce to 0 and submit a concurrent rule.
  assert.match(
    rateLimitFormPayload({
      scope: "user_path",
      subject: "/",
      period: "custom",
      period_seconds: "",
      max_requests: "5",
      max_tokens: "",
    }).error,
    /period seconds is required/i,
  );

  // Explicit 0 is only valid for the concurrent period.
  assert.match(
    rateLimitFormPayload({
      scope: "user_path",
      subject: "/",
      period: "custom",
      period_seconds: 0,
      max_requests: "5",
      max_tokens: "",
    }).error,
    /concurrent/i,
  );

  const concurrent = rateLimitFormPayload({
    scope: "user_path",
    subject: "/",
    period: "concurrent",
    period_seconds: 0,
    max_requests: "5",
    max_tokens: "",
  });
  assert.equal(concurrent.error, undefined);
  assert.equal(concurrent.payload.limit_key.period_seconds, 0);
});

test("rateLimitFormPayload handles provider and model scopes", () => {
  const provider = rateLimitFormPayload({
    scope: "provider",
    subject: "openai",
    period: "minute",
    period_seconds: 60,
    max_requests: "500",
    max_tokens: "",
  });
  assert.equal(provider.error, undefined);
  assert.equal(provider.payload.scope, "provider");
  assert.equal(provider.payload.subject, "openai");
  assert.equal(provider.payload.per_child, false);

  // A provider or model rule cannot be saved without its subject.
  assert.match(
    rateLimitFormPayload({
      scope: "provider",
      subject: "  ",
      period: "minute",
      period_seconds: 60,
      max_requests: "5",
      max_tokens: "",
    }).error,
    /provider name is required/i,
  );

  const model = rateLimitFormPayload({
    scope: "model",
    subject: "openai/gpt-4o",
    period: "minute",
    period_seconds: 60,
    max_requests: "",
    max_tokens: "100000",
  });
  assert.equal(model.error, undefined);
  assert.equal(model.payload.scope, "model");
  assert.equal(model.payload.subject, "openai/gpt-4o");
  assert.equal(model.payload.max_tokens, 100000);
});

test("rateLimitFormPayload enables independent child counters for user paths", () => {
  const result = rateLimitFormPayload({
    ...defaultRateLimitForm(),
    subject: "/users",
    max_requests: "100",
    per_child: true,
  });
  assert.equal(result.error, undefined);
  assert.equal(result.payload.per_child, true);
});

test("syncRateLimitScope resets the subject per scope", () => {
  const form = defaultRateLimitForm();

  form.per_child = true;
  form.scope = "provider";
  syncRateLimitScope(form);
  assert.equal(form.subject, "");
  assert.equal(form.per_child, false);
  assert.equal(rateLimitSubjectFieldLabel(form), "Provider Name");
  assert.equal(rateLimitSubjectPlaceholder(form), "openai");

  form.scope = "user_path";
  syncRateLimitScope(form);
  assert.equal(form.subject, "/");
  assert.equal(rateLimitSubjectFieldLabel(form), "User Path");
});

test("groupRateLimits buckets every scope and nests providers by subject", () => {
  const rules = [
    {
      scope: "user_path",
      subject: "/team",
      user_path: "/team",
      period_seconds: 60,
    },
    {
      scope: "provider",
      subject: "openai",
      period_seconds: 60,
    },
    {
      scope: "model",
      subject: "openai/gpt-4o",
      period_seconds: 60,
    },
    {
      scope: "provider",
      subject: "anthropic",
      period_seconds: 0,
    },
    {
      scope: "user_path",
      subject: "/",
      user_path: "/",
      period_seconds: 86400,
    },
    {
      scope: "model",
      subject: "openai/gpt-4o-mini",
      period_seconds: 3600,
    },
  ];

  const groups = groupRateLimits(rules);
  assert.deepEqual(
    groups.map((group) => group.scope),
    ["user_path", "provider", "model"],
  );
  assert.deepEqual(
    groups.map((group) => group.count),
    [2, 2, 2],
  );

  // user_path stays flat and subject-sorted.
  assert.deepEqual(
    groups[0].rows.map((item) => item.subject),
    ["/", "/team"],
  );
  assert.equal(groups[0].subGroups, null);

  // Provider scope is sub-grouped per provider name (alphabetical).
  assert.deepEqual(
    groups[1].subGroups.map((sub) => sub.subject),
    ["anthropic", "openai"],
  );
  assert.deepEqual(
    groups[1].subGroups.map((sub) => sub.rows.length),
    [1, 1],
  );

  // model stays flat and subject-sorted.
  assert.deepEqual(
    groups[2].rows.map((item) => item.subject),
    ["openai/gpt-4o", "openai/gpt-4o-mini"],
  );
});

test("groupRateLimits keeps every scope header even when empty", () => {
  const groups = groupRateLimits([]);
  assert.equal(groups.length, 3);
  assert.deepEqual(
    groups.map((group) => group.scope),
    ["user_path", "provider", "model"],
  );
  assert.deepEqual(
    groups.map((group) => group.count),
    [0, 0, 0],
  );
  assert.equal(groups[1].subGroups.length, 0);

  const withNull = groupRateLimits(null);
  assert.equal(withNull.length, 3);
  assert.equal(withNull[0].rows.length, 0);
});

test("scope meta falls back to user_path for unknown scopes", () => {
  assert.equal(rateLimitScopeMeta("bogus").fieldLabel, "User Path");
  assert.equal(rateLimitScopeMeta("model").chip, "model");
});

test("filteredRateLimits sorts by scope, subject, period and filters", () => {
  const rules = [
    {
      scope: "user_path",
      subject: "/team",
      user_path: "/team",
      period_seconds: 86400,
      period_label: "day",
    },
    {
      scope: "model",
      subject: "openai/gpt-4o",
      period_seconds: 60,
      period_label: "minute",
    },
    {
      scope: "user_path",
      subject: "/alpha",
      user_path: "/alpha",
      period_seconds: 60,
      period_label: "minute",
    },
    {
      scope: "provider",
      subject: "openai",
      period_seconds: 0,
      period_label: "concurrent",
    },
    {
      scope: "user_path",
      subject: "/team",
      user_path: "/team",
      period_seconds: 0,
      period_label: "concurrent",
    },
  ];
  const sorted = filteredRateLimits(rules, "");
  assert.deepEqual(
    sorted.map((item) => rateLimitKey(item)),
    [
      "user_path:/alpha:60",
      "user_path:/team:0",
      "user_path:/team:86400",
      "provider:openai:0",
      "model:openai/gpt-4o:60",
    ],
  );

  const filtered = filteredRateLimits(rules, "provider");
  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].subject, "openai");

  // Items from a pre-scope server still key off user_path.
  assert.equal(
    rateLimitKey({ user_path: "/legacy", period_seconds: 60 }),
    "user_path:/legacy:60",
  );
});

test("normalizeRateLimitListPayload tolerates malformed payloads", () => {
  assert.deepEqual(normalizeRateLimitListPayload(null), []);
  assert.deepEqual(normalizeRateLimitListPayload({}), []);
  const rules = [{ scope: "provider", subject: "openai", period_seconds: 60 }];
  assert.equal(normalizeRateLimitListPayload({ rate_limits: rules }), rules);
});

test("usage percent clamps to 0..100", () => {
  assert.equal(rateLimitUsagePercent(50, 100), 50);
  assert.equal(rateLimitUsagePercent(200, 100), 100);
  assert.equal(rateLimitUsagePercent(-5, 100), 0);
  assert.equal(rateLimitUsagePercent(5, 0), 0);
  assert.equal(rateLimitUsagePercent("x", 100), 0);
});

test("config-sourced rules are read-only", () => {
  assert.equal(rateLimitIsReadOnly({ source: "config" }), true);
  assert.equal(rateLimitIsReadOnly({ source: "manual" }), false);
  assert.equal(rateLimitSourceLabel({ source: "config" }), "config");
  assert.equal(rateLimitSourceLabel({}), "manual");
});

test("rateLimitNormalizedIdentity mirrors server normalization per scope", () => {
  assert.equal(
    rateLimitNormalizedIdentity("provider", " OpenAI ", 60),
    rateLimitNormalizedIdentity("provider", "openai", 60),
  );
  assert.equal(
    rateLimitNormalizedIdentity("model", "OpenAI/GPT-4o", 60),
    rateLimitNormalizedIdentity("model", "openai/gpt-4o", 60),
  );
  assert.equal(
    rateLimitNormalizedIdentity("user_path", "team/alpha/", 60),
    rateLimitNormalizedIdentity("user_path", "/team/alpha", 60),
  );
  assert.notEqual(
    rateLimitNormalizedIdentity("user_path", "/team", 60),
    rateLimitNormalizedIdentity("user_path", "/team", 3600),
  );
});

test("rateLimitIdentityMoved detects real moves but not respellings", () => {
  const original = {
    scope: "model",
    subject: "openai/gpt-4o",
    period_seconds: 60,
  };
  const payload = (subject, seconds) => ({
    scope: "model",
    subject,
    limit_key: { period_seconds: seconds },
  });

  assert.equal(
    rateLimitIdentityMoved(original, payload("OpenAI/GPT-4o", 60)),
    false,
  );
  assert.equal(
    rateLimitIdentityMoved(original, payload("openai/gpt-4o-mini", 60)),
    true,
  );
  assert.equal(
    rateLimitIdentityMoved(original, payload("openai/gpt-4o", 3600)),
    true,
  );

  assert.equal(rateLimitIdentityMoved(null, payload("anything", 60)), false);
});

test("inspector sections group model, provider, and global rules", () => {
  const rules = [
    { scope: "model", subject: "openai/gpt-4o", period_seconds: 60 },
    { scope: "model", subject: "gpt-4o", period_seconds: 3600 },
    { scope: "model", subject: "gpt-4o-mini", period_seconds: 60 },
    { scope: "provider", subject: "openai", period_seconds: 0 },
    { scope: "provider", subject: "anthropic", period_seconds: 60 },
    { scope: "user_path", subject: "/", user_path: "/", period_seconds: 60 },
    {
      scope: "user_path",
      subject: "/team",
      user_path: "/team",
      period_seconds: 60,
    },
  ];

  const sections = rateLimitInspectorSections(
    { kind: "model", provider: "openai", model: "GPT-4o", title: "gpt-4o" },
    rules,
  );
  assert.deepEqual(
    sections.map((section) => section.key),
    ["model", "provider", "global"],
  );
  // Both the qualified and the bare rule cover this model; the other model does not.
  assert.deepEqual(
    sections[0].items.map((item) => item.subject),
    ["openai/gpt-4o", "gpt-4o"],
  );
  assert.equal(sections[0].subject, "openai/GPT-4o");
  assert.deepEqual(
    sections[1].items.map((item) => item.subject),
    ["openai"],
  );
  // Only root user-path rules are global; /team is per-consumer.
  assert.deepEqual(
    sections[2].items.map((item) => item.subject),
    ["/"],
  );

  const providerSections = rateLimitInspectorSections(
    { kind: "provider", provider: "anthropic", model: "", title: "anthropic" },
    rules,
  );
  assert.deepEqual(
    providerSections.map((section) => section.key),
    ["provider", "global"],
  );
  assert.deepEqual(
    providerSections[0].items.map((item) => item.subject),
    ["anthropic"],
  );
});

test("pressure percent, style, and class ramp with usage", () => {
  const low = {
    period_seconds: 60,
    max_requests: 100,
    requests_used: 10,
    max_tokens: 1000,
    tokens_used: 200,
  };
  assert.equal(rateLimitPressurePercent(low), 20);
  assert.equal(rateLimitPressureStyle(low), "--rate-limit-pressure: 20%");
  assert.equal(rateLimitPressureClass(low), "rate-limit-pressure-row");

  const high = { period_seconds: 60, max_requests: 100, requests_used: 80 };
  assert.equal(
    rateLimitPressureClass(high),
    "rate-limit-pressure-row rate-limit-pressure-high",
  );

  const full = { period_seconds: 60, max_tokens: 100, tokens_used: 250 };
  assert.equal(rateLimitPressurePercent(full), 100);
  assert.equal(
    rateLimitPressureClass(full),
    "rate-limit-pressure-row rate-limit-pressure-full",
  );

  const concurrent = { period_seconds: 0, max_requests: 4, in_flight: 3 };
  assert.equal(rateLimitPressurePercent(concurrent), 75);
  assert.equal(
    rateLimitPressureClass(concurrent),
    "rate-limit-pressure-row rate-limit-pressure-high",
  );
});

test("gauge indicator distinguishes direct, inherited, and no limits", () => {
  let rules = [
    { scope: "model", subject: "openai/gpt-4o", period_seconds: 60 },
    { scope: "provider", subject: "openai", period_seconds: 60 },
  ];

  // Direct model rule → fully painted (row provider/model already normalized
  // by the store wrapper, which lowercases before delegating).
  assert.equal(
    rateLimitGaugeClassForModel(rules, "openai", "gpt-4o"),
    "table-action-btn-active",
  );
  // Only the provider rule throttles this model → half painted.
  assert.equal(
    rateLimitGaugeClassForModel(rules, "openai", "gpt-4o-mini"),
    "rate-limit-gauge-inherited",
  );
  // Nothing applies.
  assert.equal(rateLimitGaugeClassForModel(rules, "anthropic", "claude"), "");

  assert.equal(
    rateLimitGaugeClassForProvider(rules, "openai"),
    "table-action-btn-active",
  );
  assert.equal(rateLimitGaugeClassForProvider(rules, "anthropic"), "");

  // A root user-path rule throttles everything → inherited for all.
  rules = rules.concat([
    { scope: "user_path", subject: "/", user_path: "/", period_seconds: 60 },
  ]);
  assert.equal(
    rateLimitGaugeClassForModel(rules, "anthropic", "claude"),
    "rate-limit-gauge-inherited",
  );
  assert.equal(
    rateLimitGaugeClassForProvider(rules, "anthropic"),
    "rate-limit-gauge-inherited",
  );

  assert.match(
    rateLimitGaugeTitle("gpt-4o", "table-action-btn-active"),
    /direct limits configured/,
  );
  assert.match(
    rateLimitGaugeTitle("claude", "rate-limit-gauge-inherited"),
    /inherited limits apply/,
  );
  assert.equal(rateLimitGaugeTitle("claude", ""), "Rate limits for claude");
});

test("inspector summary reports in-flight or per-cap usage", () => {
  assert.equal(
    rateLimitInspectorSummary({ per_child: true }),
    "Independent counters per direct child",
  );
  assert.equal(
    rateLimitInspectorSummary({
      period_seconds: 0,
      in_flight: 3,
      max_requests: 4,
    }),
    "3 of 4 in flight",
  );
  assert.equal(
    rateLimitInspectorSummary({
      period_seconds: 60,
      max_requests: 100,
      requests_used: 10,
      max_tokens: 1000,
      tokens_used: 200,
    }),
    "10/100 req · 200/1,000 tok",
  );
  assert.equal(
    rateLimitInspectorSummary({
      period_seconds: 60,
      max_requests: 100,
      requests_used: 10,
    }),
    "10/100 req",
  );
});
