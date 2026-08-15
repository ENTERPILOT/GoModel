// Pure budget helpers, kept free of Svelte/store imports so node:test can
// exercise them directly (the relative format.js import resolves under both
// Vite and plain node).

import { formatCost } from "../../lib/utils/format.js";
import { Calendar, CalendarDays, Clock, Settings2, Sun } from "lucide";
import * as m from "../../lib/paraglide/messages.js";

export function defaultBudgetForm() {
  return {
    scope: "user_path",
    subject: "/",
    period: "daily",
    period_seconds: 86400,
    amount: "",
    per_child: false,
    source: "manual",
  };
}

// One scope metadata table drives the select options, list chips, and the
// subject field's label/placeholder, mirroring the Rate Limits page.
export function budgetScopeMeta(scope) {
  const meta = {
    user_path: {
      label: m.budgets_scope_user_path(),
      chip: m.budgets_scope_user_path_chip(),
      fieldLabel: m.budgets_scope_user_path_field(),
      placeholder: "/team/alpha",
    },
    label: {
      label: m.budgets_scope_label(),
      chip: m.budgets_scope_label_chip(),
      fieldLabel: m.budgets_scope_label(),
      placeholder: "Mobile-App-iOS",
    },
  };
  return meta[scope] || meta.user_path;
}

export function budgetScopeOptions() {
  return ["user_path", "label"].map((scope) => ({
    value: scope,
    label: budgetScopeMeta(scope).label,
  }));
}

export function budgetScope(item) {
  const scope = String((item && item.scope) || "").trim();
  return scope || "user_path";
}

export function budgetSubject(item) {
  const subject = String((item && item.subject) || "").trim();
  return subject || String((item && item.user_path) || "");
}

export function budgetScopeLabel(item) {
  return budgetScopeMeta(budgetScope(item)).chip;
}

// budgetScopeValueClass names the scope modifier for the shared
// .budget-scope-value subject styling.
export function budgetScopeValueClass(item) {
  return budgetScope(item) === "label" ? "budget-label" : "budget-user-path";
}

export function budgetSubjectFieldLabel(form) {
  return budgetScopeMeta(String((form && form.scope) || "")).fieldLabel;
}

export function budgetSubjectPlaceholder(form) {
  return budgetScopeMeta(String((form && form.scope) || "")).placeholder;
}

// Changing scope resets the subject: a user path never carries over to a
// label. Mutates the form in place.
export function syncBudgetScope(form) {
  form.subject = String((form && form.scope) || "") === "user_path" ? "/" : "";
  form.per_child = false;
}

export function budgetPeriodOptions() {
  return [
    { value: "hourly", label: m.budgets_period_hourly() },
    { value: "daily", label: m.budgets_period_daily() },
    { value: "weekly", label: m.budgets_period_weekly() },
    { value: "monthly", label: m.budgets_period_monthly() },
    { value: "custom", label: m.budgets_period_custom() },
  ];
}

export function budgetPeriodSeconds(period) {
  switch (
    String(period || "")
      .trim()
      .toLowerCase()
  ) {
    case "hourly":
      return 3600;
    case "daily":
      return 86400;
    case "weekly":
      return 604800;
    case "monthly":
      return 2592000;
    default:
      return 0;
  }
}

export function budgetPeriodFromSeconds(seconds) {
  switch (Number(seconds || 0)) {
    case 3600:
      return "hourly";
    case 86400:
      return "daily";
    case 604800:
      return "weekly";
    case 2592000:
      return "monthly";
    default:
      return "custom";
  }
}

export function budgetKey(item) {
  return (
    budgetScope(item) +
    ":" +
    budgetSubject(item) +
    ":" +
    String((item && item.period_seconds) || "")
  );
}

export function findExistingBudget(budgets, payload) {
  if (!payload || !Array.isArray(budgets)) {
    return null;
  }
  const key = budgetKey(payload);
  return budgets.find((item) => budgetKey(item) === key) || null;
}

export function budgetUserPathValidationError(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return m.budgets_user_path_required();
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const segments = raw.split("/");
  for (const part of segments) {
    const segment = String(part || "").trim();
    if (!segment) {
      continue;
    }
    if (segment === "." || segment === "..") {
      return m.budgets_user_path_segments();
    }
    if (segment.includes(":")) {
      return m.budgets_user_path_colon();
    }
  }
  return "";
}

export function normalizeBudgetUserPath(value) {
  if (budgetUserPathValidationError(value)) {
    return "";
  }
  const trimmed = String(value || "").trim();
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const segments = raw.split("/");
  const canonical = [];
  for (const part of segments) {
    const segment = String(part || "").trim();
    if (segment) {
      canonical.push(segment);
    }
  }
  return canonical.length ? "/" + canonical.join("/") : "/";
}

// budgetInputUserPath keeps the form input controlled with a leading slash.
export function budgetInputUserPath(value) {
  const body = String(value || "")
    .trimStart()
    .replace(/^\/+/, "");
  return "/" + body;
}

export function normalizeBudgetListPayload(payload) {
  if (Array.isArray(payload)) {
    return payload;
  }
  if (payload && Array.isArray(payload.budgets)) {
    return payload.budgets;
  }
  return [];
}

function budgetFilterText(item) {
  const seconds = Number((item && item.period_seconds) || 0);
  return [
    budgetSubject(item),
    budgetScopeLabel(item),
    budgetPeriodLabel(item),
    budgetPeriodFromSeconds(seconds),
    item && item.per_child ? m.budgets_per_child_badge() : "",
    seconds ? String(seconds) + "s" : "",
    seconds ? String(seconds) + " seconds" : "",
  ]
    .join(" ")
    .toLowerCase();
}

const budgetScopeOrder = { user_path: 0, label: 1 };

function sortBudgets(items, sortBy) {
  const sorted = Array.isArray(items) ? items.slice() : [];
  const by = String(sortBy || "subject");
  sorted.sort((a, b) => {
    const scopeCompare =
      (budgetScopeOrder[budgetScope(a)] || 0) -
      (budgetScopeOrder[budgetScope(b)] || 0);
    const subjectCompare = budgetSubject(a).localeCompare(budgetSubject(b));
    const periodCompare =
      Number((b && b.period_seconds) || 0) -
      Number((a && a.period_seconds) || 0);
    if (by === "period") {
      return periodCompare || scopeCompare || subjectCompare;
    }
    return scopeCompare || subjectCompare || periodCompare;
  });
  return sorted;
}

export function filterAndSortBudgets(budgets, filter, sortBy) {
  const list = Array.isArray(budgets) ? budgets : [];
  const needle = String(filter || "")
    .trim()
    .toLowerCase();
  const items = needle
    ? list.filter((item) => budgetFilterText(item).includes(needle))
    : list.slice();
  return sortBudgets(items, sortBy);
}

// buildBudgetFormPayload validates the editor form and returns
// { payload, error } — payload is null when validation fails.
export function buildBudgetFormPayload(form) {
  const f = form || {};
  const scope = budgetScope(f);
  const subject = String(f.subject || "").trim();
  if (scope === "user_path") {
    const userPathError = budgetUserPathValidationError(subject);
    if (userPathError) {
      return { payload: null, error: userPathError };
    }
  } else if (!subject) {
    return { payload: null, error: m.budgets_label_required() };
  }
  const amount = Number(f.amount);
  if (!Number.isFinite(amount) || amount <= 0) {
    return { payload: null, error: m.budgets_amount_positive() };
  }
  const period = String(f.period || "").trim();
  let periodSeconds = budgetPeriodSeconds(period);
  if (period === "custom") {
    periodSeconds = Number(f.period_seconds);
  }
  if (!Number.isFinite(periodSeconds) || periodSeconds <= 0) {
    return { payload: null, error: m.budgets_period_positive() };
  }
  return {
    payload: {
      scope,
      subject:
        scope === "user_path" ? normalizeBudgetUserPath(subject) : subject,
      period_seconds: Math.trunc(periodSeconds),
      amount,
      per_child: scope === "user_path" && Boolean(f.per_child),
      source: String(f.source || "manual").trim() || "manual",
    },
    error: "",
  };
}

// Request bodies for the /admin/budgets endpoints (the exact shapes the
// API expects).
export function budgetPutBody(payload) {
  return {
    scope: budgetScope(payload),
    subject: budgetSubject(payload),
    budget_key: { period_seconds: payload.period_seconds },
    amount: payload.amount,
    per_child: Boolean(payload.per_child),
  };
}

export function budgetDeleteBody(item) {
  return {
    scope: budgetScope(item),
    subject: budgetSubject(item),
    budget_key: { period_seconds: item.period_seconds },
  };
}

export function budgetResetOneBody(item) {
  return {
    scope: budgetScope(item),
    subject: budgetSubject(item),
    period_seconds: item.period_seconds,
  };
}

export function budgetAmountLabel(value) {
  return formatCost(value);
}

export function budgetOverrideDialogMessage(payload, existing) {
  const p = payload || {};
  const e = existing || {};
  const label =
    (budgetSubject(p) || budgetSubject(e)) +
    " " +
    budgetPeriodLabel({
      period_seconds: p.period_seconds || e.period_seconds,
      period_label: e.period_label,
    });
  return m.budgets_override_message({
    label,
    current: budgetAmountLabel(e.amount),
    next: budgetAmountLabel(p.amount),
  });
}

function budgetRatio(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) {
    return 0;
  }
  return parsed;
}

function budgetPercentFromRatio(value, clamp) {
  const ratio = budgetRatio(value);
  const bounded = clamp ? Math.min(ratio, 1) : ratio;
  return Math.round(bounded * 1000) / 10;
}

export function budgetUsageRatio(item) {
  return budgetRatio(item && item.usage_ratio);
}

export function budgetUsagePercent(item) {
  return budgetPercentFromRatio(budgetUsageRatio(item), true);
}

export function budgetPeriodPercent(item) {
  return budgetPercentFromRatio(item && item.period_ratio, true);
}

export function budgetUsagePercentLabel(item) {
  return (
    budgetPercentFromRatio(budgetUsageRatio(item), false)
      .toFixed(1)
      .replace(/\.0$/, "") + "%"
  );
}

export function budgetPeriodPercentLabel(item) {
  return budgetPeriodPercent(item).toFixed(1).replace(/\.0$/, "") + "%";
}

export function budgetPeriodLabel(item) {
  const seconds = Number((item && item.period_seconds) || 0);
  switch (seconds) {
    case 3600:
      return m.budgets_period_hourly();
    case 86400:
      return m.budgets_period_daily();
    case 604800:
      return m.budgets_period_weekly();
    case 2592000:
      return m.budgets_period_monthly();
    default: {
      const label = String((item && item.period_label) || "").trim();
      return m.budgets_custom_period({ period: label || String(seconds || "") + "s" });
    }
  }
}

export function budgetPeriodClass(item) {
  const seconds = Number((item && item.period_seconds) || 0);
  switch (seconds) {
    case 3600:
      return "budget-period-label-hourly";
    case 86400:
      return "budget-period-label-daily";
    case 604800:
      return "budget-period-label-weekly";
    case 2592000:
      return "budget-period-label-monthly";
    default:
      return "budget-period-label-custom";
  }
}

export function budgetPeriodBarClass(item) {
  return budgetPeriodClass(item).replace(
    "budget-period-label-",
    "budget-bar-fill-period-",
  );
}

export function budgetPeriodTrackClass(item) {
  return budgetPeriodClass(item).replace(
    "budget-period-label-",
    "budget-bar-track-period-",
  );
}

export function budgetPeriodIcon(item) {
  const seconds = Number((item && item.period_seconds) || 0);
  switch (seconds) {
    case 3600:
      return Clock;
    case 86400:
      return Sun;
    case 604800:
      return CalendarDays;
    case 2592000:
      return Calendar;
    default:
      return Settings2;
  }
}

function formatBudgetPeriodSeconds(seconds) {
  const normalized = Math.max(0, Math.trunc(Number(seconds || 0)));
  return normalized === 1
    ? m.budgets_one_second()
    : m.budgets_seconds({ count: normalized });
}

export function budgetPeriodDurationLabel(item) {
  const seconds = Number((item && item.period_seconds) || 0);
  switch (seconds) {
    case 3600:
      return m.budgets_one_hour();
    case 86400:
      return m.budgets_one_day();
    case 604800:
      return m.budgets_one_week();
    case 2592000:
      return m.budgets_one_month();
    default:
      return formatBudgetPeriodSeconds(seconds);
  }
}

export function budgetSourceLabel(item) {
  const source = String((item && item.source) || "").trim();
  return source || "manual";
}

export function budgetSourceTitle(item) {
  const source = budgetSourceLabel(item).toLowerCase();
  if (source === "manual") {
    return m.budgets_source_manual();
  }
  if (source === "config") {
    return m.budgets_source_config();
  }
  return m.budgets_source_other({ source });
}

export function budgetRemainingLabel(item) {
  const remaining = Number(item && item.remaining);
  if (!Number.isFinite(remaining)) {
    return "";
  }
  if (remaining < 0) {
    return m.budgets_over({ amount: formatCost(Math.abs(remaining)) });
  }
  return m.budgets_remaining({ amount: formatCost(remaining) });
}
