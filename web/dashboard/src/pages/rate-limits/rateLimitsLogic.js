// Pure rate-limit logic shared by the rateLimits store, the Rate Limits page,
// and the Models-page gauge/inspector. These functions are also exercised by
// tests/rate-limits.test.js under plain node.

export function defaultRateLimitForm() {
  return {
    scope: "user_path",
    subject: "/",
    period: "minute",
    period_seconds: 60,
    max_requests: "",
    max_tokens: "",
    per_child: false,
    source: "manual",
  };
}

// One scope metadata table drives the select options, list chips,
// and the subject field's label/placeholder.
export function rateLimitScopeMeta(scope) {
  const meta = {
    user_path: {
      label: "User path",
      chip: "user path",
      fieldLabel: "User Path",
      placeholder: "/team/alpha",
    },
    provider: {
      label: "Provider",
      chip: "provider",
      fieldLabel: "Provider Name",
      placeholder: "openai",
    },
    model: {
      label: "Model",
      chip: "model",
      fieldLabel: "Model",
      placeholder: "openai/gpt-4o",
    },
  };
  return meta[scope] || meta.user_path;
}

export function rateLimitScopeOptions() {
  return ["user_path", "provider", "model"].map((scope) => ({
    value: scope,
    label: rateLimitScopeMeta(scope).label,
  }));
}

export function rateLimitScope(item) {
  const scope = String((item && item.scope) || "").trim();
  return scope || "user_path";
}

export function rateLimitSubject(item) {
  const subject = String((item && item.subject) || "").trim();
  return subject || String((item && item.user_path) || "");
}

export function rateLimitScopeLabel(item) {
  return rateLimitScopeMeta(rateLimitScope(item)).chip;
}

export function rateLimitSubjectFieldLabel(form) {
  return rateLimitScopeMeta(String((form && form.scope) || "")).fieldLabel;
}

export function rateLimitSubjectPlaceholder(form) {
  return rateLimitScopeMeta(String((form && form.scope) || "")).placeholder;
}

// Changing scope resets the subject: a user path never carries
// over to a provider or model rule. Mutates the form in place.
export function syncRateLimitScope(form) {
  const scope = String((form && form.scope) || "");
  form.subject = scope === "user_path" ? "/" : "";
  form.per_child = false;
}

export function rateLimitPeriodOptions() {
  return [
    { value: "minute", label: "Per minute" },
    { value: "hour", label: "Per hour" },
    { value: "day", label: "Per day" },
    { value: "concurrent", label: "Concurrent (in-flight)" },
    { value: "custom", label: "Custom seconds" },
  ];
}

export function rateLimitPeriodSeconds(period) {
  switch (
    String(period || "")
      .trim()
      .toLowerCase()
  ) {
    case "minute":
      return 60;
    case "hour":
      return 3600;
    case "day":
      return 86400;
    case "concurrent":
      return 0;
    default:
      return -1;
  }
}

export function rateLimitPeriodFromSeconds(seconds) {
  switch (Number(seconds || 0)) {
    case 60:
      return "minute";
    case 3600:
      return "hour";
    case 86400:
      return "day";
    case 0:
      return "concurrent";
    default:
      return "custom";
  }
}

// Mutates the form: maps the named period to seconds and drops the token
// limit the concurrent period hides so it cannot block the save invisibly.
export function syncRateLimitPeriodSeconds(form) {
  const period = String((form && form.period) || "").trim();
  const seconds = rateLimitPeriodSeconds(period);
  if (seconds >= 0) {
    form.period_seconds = seconds;
  }
  if (period === "concurrent") {
    form.max_tokens = "";
  }
}

export function rateLimitKey(item) {
  return (
    rateLimitScope(item) +
    ":" +
    rateLimitSubject(item) +
    ":" +
    String((item && item.period_seconds) || "0")
  );
}

export function rateLimitIsConcurrent(item) {
  return Number((item && item.period_seconds) || 0) === 0;
}

export function rateLimitPeriodLabel(item) {
  const label = String((item && item.period_label) || "").trim();
  if (label) {
    return label;
  }
  return rateLimitPeriodFromSeconds(Number((item && item.period_seconds) || 0));
}

export function rateLimitSourceLabel(item) {
  return String((item && item.source) || "") === "config" ? "config" : "manual";
}

export function rateLimitIsReadOnly(item) {
  return String((item && item.source) || "") === "config";
}

export function formatRateLimitNumber(value) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) {
    return "0";
  }
  return numeric.toLocaleString();
}

export function rateLimitUsagePercent(used, limit) {
  const usedNum = Number(used);
  const limitNum = Number(limit);
  if (
    !Number.isFinite(usedNum) ||
    !Number.isFinite(limitNum) ||
    limitNum <= 0
  ) {
    return 0;
  }
  const percent = Math.round((usedNum / limitNum) * 100);
  return Math.min(Math.max(percent, 0), 100);
}

export function filteredRateLimits(rules, filter) {
  const needle = String(filter || "")
    .trim()
    .toLowerCase();
  const items = Array.isArray(rules) ? rules.slice() : [];
  const scopeOrder = { user_path: 0, provider: 1, model: 2 };
  items.sort((a, b) => {
    const scopeCompare =
      (scopeOrder[rateLimitScope(a)] || 0) -
      (scopeOrder[rateLimitScope(b)] || 0);
    if (scopeCompare !== 0) {
      return scopeCompare;
    }
    const subjectCompare = rateLimitSubject(a).localeCompare(
      rateLimitSubject(b),
    );
    if (subjectCompare !== 0) {
      return subjectCompare;
    }
    return Number(a.period_seconds || 0) - Number(b.period_seconds || 0);
  });
  if (!needle) {
    return items;
  }
  return items.filter((item) => {
    const subject = rateLimitSubject(item).toLowerCase();
    const scope = rateLimitScopeLabel(item).toLowerCase();
    const period = rateLimitPeriodLabel(item).toLowerCase();
    const mode = item && item.per_child ? "per child" : "";
    return (
      subject.includes(needle) ||
      scope.includes(needle) ||
      period.includes(needle) ||
      mode.includes(needle)
    );
  });
}

export function normalizeRateLimitListPayload(payload) {
  if (!payload || !Array.isArray(payload.rate_limits)) {
    return [];
  }
  return payload.rate_limits;
}

// Mirrors the server's per-scope subject normalization so an edit
// that only respells the same identity (case, slashes) is treated
// as an in-place update, never a move-plus-delete of itself.
export function rateLimitNormalizedIdentity(scope, subject, periodSeconds) {
  let normalized = String(subject || "").trim();
  if (scope === "provider" || scope === "model") {
    normalized = normalized.toLowerCase();
  } else {
    const segments = normalized
      .split("/")
      .map((part) => part.trim())
      .filter(Boolean);
    normalized = "/" + segments.join("/");
  }
  return scope + ":" + normalized + ":" + Number(periodSeconds || 0);
}

export function rateLimitIdentityMoved(original, payload) {
  if (!original) {
    return false;
  }
  return (
    rateLimitNormalizedIdentity(
      payload.scope,
      payload.subject,
      payload.limit_key.period_seconds,
    ) !==
    rateLimitNormalizedIdentity(
      original.scope,
      original.subject,
      original.period_seconds,
    )
  );
}

// Validates the editor form and builds the PUT /admin/rate-limits payload.
// Returns { payload } on success or { error } on validation failure.
export function rateLimitFormPayload(formInput) {
  const form = formInput || {};
  const scope = String(form.scope || "user_path");
  const subject = String(form.subject || "").trim();
  if (scope !== "user_path" && !subject) {
    return { error: rateLimitSubjectFieldLabel(form) + " is required." };
  }
  const isConcurrent = String(form.period || "") === "concurrent";
  // Reject blank custom seconds before Number(): Number('') is 0,
  // which would silently submit a concurrent rule.
  const rawPeriodSeconds = form.period_seconds;
  if (
    rawPeriodSeconds === "" ||
    rawPeriodSeconds === null ||
    rawPeriodSeconds === undefined
  ) {
    return { error: "Period seconds is required." };
  }
  const periodSeconds = Number(rawPeriodSeconds);
  if (
    !Number.isInteger(periodSeconds) ||
    periodSeconds < 0 ||
    (periodSeconds === 0 && !isConcurrent)
  ) {
    return {
      error:
        "Period seconds must be a positive integer (0 only for the concurrent period).",
    };
  }
  const maxRequests = String(
    form.max_requests === undefined || form.max_requests === null
      ? ""
      : form.max_requests,
  ).trim();
  const maxTokens = String(
    form.max_tokens === undefined || form.max_tokens === null
      ? ""
      : form.max_tokens,
  ).trim();
  if (!maxRequests && !maxTokens) {
    return { error: "Set max requests, max tokens, or both." };
  }
  if (isConcurrent && maxTokens) {
    return { error: "Token limits are not valid for the concurrent period." };
  }
  const payload = {
    scope: scope,
    subject: subject || "/",
    per_child: scope === "user_path" && Boolean(form.per_child),
    limit_key: { period_seconds: periodSeconds },
  };
  if (maxRequests) {
    const parsed = Number(maxRequests);
    if (!Number.isInteger(parsed) || parsed <= 0) {
      return { error: "Max requests must be a positive integer." };
    }
    payload.max_requests = parsed;
  }
  if (maxTokens) {
    const parsed = Number(maxTokens);
    if (!Number.isInteger(parsed) || parsed <= 0) {
      return { error: "Max tokens must be a positive integer." };
    }
    payload.max_tokens = parsed;
  }
  return { payload };
}

// --- Effective-limits inspector / Models-page gauges ---

export function rateLimitRuleMatchesModel(rule, provider, model) {
  if (rateLimitScope(rule) !== "model") {
    return false;
  }
  const subject = String(rateLimitSubject(rule)).toLowerCase();
  const bare = String(model || "")
    .trim()
    .toLowerCase();
  if (!bare) {
    return false;
  }
  if (subject === bare) {
    return true;
  }
  const prov = String(provider || "")
    .trim()
    .toLowerCase();
  if (!prov) {
    return false;
  }
  if (subject === prov + "/" + bare) {
    return true;
  }
  return bare.startsWith(prov + "/") && subject === bare.slice(prov.length + 1);
}

export function rateLimitRuleMatchesProvider(rule, provider) {
  return (
    rateLimitScope(rule) === "provider" &&
    String(rateLimitSubject(rule)).toLowerCase() ===
      String(provider || "")
        .trim()
        .toLowerCase()
  );
}

export function rateLimitInspectorQualifiedModel(inspectorInput) {
  const inspector = inspectorInput || {};
  const model = String(inspector.model || "");
  const provider = String(inspector.provider || "");
  if (!provider || model.toLowerCase().startsWith(provider + "/")) {
    return model;
  }
  return provider + "/" + model;
}

export function rateLimitInspectorSections(inspectorInput, rulesInput) {
  const inspector = inspectorInput || {};
  const rules = Array.isArray(rulesInput) ? rulesInput : [];
  const sections = [];
  if (inspector.kind === "model") {
    sections.push({
      key: "model",
      title: "Model limits",
      scope: "model",
      subject: rateLimitInspectorQualifiedModel(inspector),
      hint: "",
      items: rules.filter((rule) =>
        rateLimitRuleMatchesModel(rule, inspector.provider, inspector.model),
      ),
    });
  }
  sections.push({
    key: "provider",
    title: "Provider limits (" + inspector.provider + ")",
    scope: "provider",
    subject: inspector.provider,
    hint:
      inspector.kind === "model"
        ? "Shared by every model routed to this provider."
        : "",
    items: rules.filter((rule) =>
      rateLimitRuleMatchesProvider(rule, inspector.provider),
    ),
  });
  sections.push({
    key: "global",
    title: "Global limits",
    scope: "user_path",
    subject: "/",
    hint: "Root user-path rules throttle all traffic. Narrower user-path rules also apply, per consumer.",
    items: rules.filter(
      (rule) =>
        rateLimitScope(rule) === "user_path" && rateLimitSubject(rule) === "/",
    ),
  });
  return sections;
}

// rateLimitPressurePercent reports how close a rule is to its most
// constrained cap (0..100), across requests, tokens, and in-flight.
export function rateLimitPressurePercent(item) {
  if (rateLimitIsConcurrent(item)) {
    return rateLimitUsagePercent(item.in_flight, item.max_requests);
  }
  return Math.max(
    rateLimitUsagePercent(item.requests_used, item.max_requests),
    rateLimitUsagePercent(item.tokens_used, item.max_tokens),
  );
}

export function rateLimitPressureStyle(item) {
  return "--rate-limit-pressure: " + rateLimitPressurePercent(item) + "%";
}

export function rateLimitPressureClass(item) {
  const percent = rateLimitPressurePercent(item);
  if (percent >= 100) {
    return "rate-limit-pressure-row rate-limit-pressure-full";
  }
  if (percent >= 75) {
    return "rate-limit-pressure-row rate-limit-pressure-high";
  }
  return "rate-limit-pressure-row";
}

export function hasGlobalRateLimits(rulesInput) {
  const rules = Array.isArray(rulesInput) ? rulesInput : [];
  return rules.some(
    (rule) =>
      rateLimitScope(rule) === "user_path" && rateLimitSubject(rule) === "/",
  );
}

// Gauge indicator states on the Models page: fully painted when the
// subject has its own rules, half painted when only provider or
// global rules throttle it, plain otherwise.
export function rateLimitGaugeClassForModel(rulesInput, provider, model) {
  const rules = Array.isArray(rulesInput) ? rulesInput : [];
  if (rules.some((rule) => rateLimitRuleMatchesModel(rule, provider, model))) {
    return "table-action-btn-active";
  }
  if (
    rules.some((rule) => rateLimitRuleMatchesProvider(rule, provider)) ||
    hasGlobalRateLimits(rules)
  ) {
    return "rate-limit-gauge-inherited";
  }
  return "";
}

export function rateLimitGaugeClassForProvider(rulesInput, provider) {
  const rules = Array.isArray(rulesInput) ? rulesInput : [];
  if (rules.some((rule) => rateLimitRuleMatchesProvider(rule, provider))) {
    return "table-action-btn-active";
  }
  return hasGlobalRateLimits(rules) ? "rate-limit-gauge-inherited" : "";
}

export function rateLimitGaugeTitle(subject, gaugeClass) {
  const base = "Rate limits for " + subject;
  if (gaugeClass === "table-action-btn-active") {
    return base + " (direct limits configured)";
  }
  if (gaugeClass) {
    return base + " (inherited limits apply)";
  }
  return base;
}

export function rateLimitInspectorSummary(item) {
  if (item && item.per_child) {
    return "Independent counters per direct child";
  }
  if (rateLimitIsConcurrent(item)) {
    return (
      formatRateLimitNumber(item.in_flight) +
      " of " +
      formatRateLimitNumber(item.max_requests) +
      " in flight"
    );
  }
  const parts = [];
  if (item.max_requests !== null && item.max_requests !== undefined) {
    parts.push(
      formatRateLimitNumber(item.requests_used) +
        "/" +
        formatRateLimitNumber(item.max_requests) +
        " req",
    );
  }
  if (item.max_tokens !== null && item.max_tokens !== undefined) {
    parts.push(
      formatRateLimitNumber(item.tokens_used) +
        "/" +
        formatRateLimitNumber(item.max_tokens) +
        " tok",
    );
  }
  return parts.join(" · ");
}
