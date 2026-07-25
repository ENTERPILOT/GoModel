// Pure-logic tests for the budgets page helpers, ported from the legacy
// internal/admin/dashboard/static/js/modules/budgets.test.cjs (page portion;
// the budget-settings cases belong to the settings page).
import test from "node:test";
import assert from "node:assert/strict";

import {
  budgetAmountLabel,
  budgetDeleteBody,
  budgetInputUserPath,
  budgetKey,
  budgetOverrideDialogMessage,
  budgetPeriodBarClass,
  budgetPeriodClass,
  budgetPeriodDurationLabel,
  budgetPeriodFromSeconds,
  budgetPeriodIcon,
  budgetPeriodLabel,
  budgetPeriodTrackClass,
  budgetPutBody,
  budgetResetOneBody,
  budgetScope,
  budgetScopeLabel,
  budgetScopeOptions,
  budgetSourceTitle,
  budgetSubject,
  budgetSubjectFieldLabel,
  budgetSubjectPlaceholder,
  budgetUserPathValidationError,
  buildBudgetFormPayload,
  defaultBudgetForm,
  filterAndSortBudgets,
  findExistingBudget,
  normalizeBudgetListPayload,
  normalizeBudgetUserPath,
  syncBudgetScope,
} from "../src/pages/budgets/budgets-helpers.js";

test("buildBudgetFormPayload normalizes user path and standard periods", () => {
  const { payload, error } = buildBudgetFormPayload({
    scope: "user_path",
    subject: "team/alpha",
    period: "weekly",
    period_seconds: 0,
    amount: "12.3456",
    source: "",
  });

  assert.equal(error, "");
  assert.equal(
    JSON.stringify(payload),
    JSON.stringify({
      scope: "user_path",
      subject: "/team/alpha",
      period_seconds: 604800,
      amount: 12.3456,
      source: "manual",
    }),
  );
});

test("buildBudgetFormPayload keeps a label subject verbatim", () => {
  const { payload, error } = buildBudgetFormPayload({
    scope: "label",
    subject: "  Mobile-App-iOS  ",
    period: "monthly",
    amount: "500",
    source: "manual",
  });

  assert.equal(error, "");
  // Only surrounding whitespace is trimmed: labels are matched exactly, so the
  // casing must survive and no leading slash may be added.
  assert.equal(payload.scope, "label");
  assert.equal(payload.subject, "Mobile-App-iOS");

  const blank = buildBudgetFormPayload({
    scope: "label",
    subject: "   ",
    period: "daily",
    amount: "5",
  });
  assert.equal(blank.payload, null);
  assert.equal(blank.error, "Label is required.");
});

test("budget scope metadata drives the selector and subject field", () => {
  assert.deepEqual(budgetScopeOptions(), [
    { value: "user_path", label: "User path" },
    { value: "label", label: "Label" },
  ]);
  assert.equal(budgetScopeLabel({ scope: "label" }), "label");
  // A budget without an explicit scope predates label budgets: treat it as a
  // user-path budget, and read its subject from the legacy user_path field.
  assert.equal(budgetScope({}), "user_path");
  assert.equal(budgetSubject({ user_path: "/team" }), "/team");
  assert.equal(budgetSubject({ scope: "label", subject: "iOS" }), "iOS");
  assert.equal(budgetSubjectFieldLabel({ scope: "label" }), "Label");
  assert.equal(budgetSubjectPlaceholder({ scope: "user_path" }), "/team/alpha");
});

test("syncBudgetScope resets the subject so a path is never sent as a label", () => {
  const form = { scope: "label", subject: "/team" };
  syncBudgetScope(form);
  assert.equal(form.subject, "");

  form.scope = "user_path";
  syncBudgetScope(form);
  assert.equal(form.subject, "/");
});

test("buildBudgetFormPayload rejects invalid amounts and custom seconds", () => {
  const emptyAmount = buildBudgetFormPayload({
    ...defaultBudgetForm(),
    amount: "",
  });
  assert.equal(emptyAmount.payload, null);
  assert.equal(emptyAmount.error, "Amount must be greater than 0.");

  const badSeconds = buildBudgetFormPayload({
    scope: "user_path",
    subject: "/team",
    period: "custom",
    period_seconds: 0,
    amount: "5",
    source: "manual",
  });
  assert.equal(badSeconds.payload, null);
  assert.equal(badSeconds.error, "Period seconds must be greater than 0.");

  const badPath = buildBudgetFormPayload({
    ...defaultBudgetForm(),
    subject: "/team/../oops",
    amount: "5",
  });
  assert.equal(badPath.payload, null);
  assert.equal(badPath.error, 'User path cannot contain "." or ".." segments.');
});

test("user path validation and normalization mirror the legacy rules", () => {
  assert.equal(budgetUserPathValidationError(""), "User path is required.");
  assert.equal(
    budgetUserPathValidationError("/a/:b"),
    'User path cannot contain ":" segments.',
  );
  assert.equal(budgetUserPathValidationError("/team/alpha"), "");
  assert.equal(normalizeBudgetUserPath("team//alpha/"), "/team/alpha");
  assert.equal(normalizeBudgetUserPath("/"), "/");
});

test("budgetInputUserPath keeps the form input controlled with a leading slash", () => {
  assert.equal(defaultBudgetForm().subject, "/");
  assert.equal(budgetInputUserPath("team/alpha"), "/team/alpha");
  assert.equal(budgetInputUserPath("/platform/service"), "/platform/service");
  assert.equal(budgetInputUserPath(""), "/");
});

test("budgetAmountLabel uses the data placeholder for invalid amounts", () => {
  assert.equal(budgetAmountLabel(null), "---");
  assert.equal(budgetAmountLabel(undefined), "---");
  assert.equal(budgetAmountLabel("abc"), "---");
  assert.equal(budgetAmountLabel(10), "$10.00");
});

test("normalizeBudgetListPayload reads budget rows from the list envelope", () => {
  const rows = [{ user_path: "/team", period_seconds: 86400, amount: 10 }];
  assert.deepEqual(normalizeBudgetListPayload({ budgets: rows }), rows);
  assert.deepEqual(normalizeBudgetListPayload(rows), rows);
  assert.deepEqual(normalizeBudgetListPayload(null), []);
  assert.deepEqual(normalizeBudgetListPayload({}), []);
});

test("findExistingBudget matches on the scope + subject + period key", () => {
  const budgets = [
    { scope: "user_path", subject: "/team", period_seconds: 86400, amount: 10 },
    { scope: "user_path", subject: "/team", period_seconds: 3600, amount: 2 },
    { scope: "label", subject: "/team", period_seconds: 86400, amount: 7 },
  ];
  const payload = {
    scope: "user_path",
    subject: "/team",
    period_seconds: 86400,
    amount: 12.5,
  };

  assert.equal(budgetKey(payload), "user_path:/team:86400");
  assert.equal(findExistingBudget(budgets, payload), budgets[0]);
  // Same subject spelling, different scope: a distinct budget.
  assert.equal(
    findExistingBudget(budgets, { scope: "label", subject: "/team", period_seconds: 86400 }),
    budgets[2],
  );
  assert.equal(
    findExistingBudget(budgets, { scope: "user_path", subject: "/other", period_seconds: 86400 }),
    null,
  );
});

test("budgetOverrideDialogMessage describes the override", () => {
  const existing = {
    user_path: "/team",
    period_seconds: 86400,
    amount: 10,
    period_label: "daily",
  };
  const payload = {
    user_path: "/team",
    period_seconds: 86400,
    amount: 12.5,
    source: "manual",
  };
  const message = budgetOverrideDialogMessage(payload, existing);

  assert.match(message, /A budget for "\/team Daily" already exists/);
  assert.match(message, /override the current \$10\.00 limit with \$12\.50/);
});

test("filterAndSortBudgets filters by user path or period and applies selected sorting", () => {
  const budgets = [
    { user_path: "/team/beta", period_seconds: 604800, period_label: "weekly" },
    { user_path: "/team/alpha", period_seconds: 86400, period_label: "daily" },
    { user_path: "/platform", period_seconds: 2592000, period_label: "monthly" },
    { user_path: "/team/alpha", period_seconds: 2592000, period_label: "monthly" },
    { user_path: "/experiments", period_seconds: 777777, period_label: "777777s" },
  ];

  assert.equal(
    JSON.stringify(filterAndSortBudgets(budgets, "TEAM/A", "user_path")),
    JSON.stringify([
      { user_path: "/team/alpha", period_seconds: 2592000, period_label: "monthly" },
      { user_path: "/team/alpha", period_seconds: 86400, period_label: "daily" },
    ]),
  );

  assert.equal(
    JSON.stringify(filterAndSortBudgets(budgets, "weekly", "user_path")),
    JSON.stringify([
      { user_path: "/team/beta", period_seconds: 604800, period_label: "weekly" },
    ]),
  );

  assert.equal(
    JSON.stringify(filterAndSortBudgets(budgets, "custom", "user_path")),
    JSON.stringify([
      { user_path: "/experiments", period_seconds: 777777, period_label: "777777s" },
    ]),
  );

  assert.equal(
    JSON.stringify(filterAndSortBudgets(budgets, "", "user_path")),
    JSON.stringify([
      { user_path: "/experiments", period_seconds: 777777, period_label: "777777s" },
      { user_path: "/platform", period_seconds: 2592000, period_label: "monthly" },
      { user_path: "/team/alpha", period_seconds: 2592000, period_label: "monthly" },
      { user_path: "/team/alpha", period_seconds: 86400, period_label: "daily" },
      { user_path: "/team/beta", period_seconds: 604800, period_label: "weekly" },
    ]),
  );

  assert.equal(
    JSON.stringify(filterAndSortBudgets(budgets, "", "period")),
    JSON.stringify([
      { user_path: "/platform", period_seconds: 2592000, period_label: "monthly" },
      { user_path: "/team/alpha", period_seconds: 2592000, period_label: "monthly" },
      { user_path: "/experiments", period_seconds: 777777, period_label: "777777s" },
      { user_path: "/team/beta", period_seconds: 604800, period_label: "weekly" },
      { user_path: "/team/alpha", period_seconds: 86400, period_label: "daily" },
    ]),
  );
});

test("filterAndSortBudgets groups user-path budgets before label budgets", () => {
  const budgets = [
    { scope: "label", subject: "iOS", period_seconds: 86400 },
    { scope: "user_path", subject: "/team", period_seconds: 86400 },
  ];

  assert.deepEqual(
    filterAndSortBudgets(budgets, "", "subject").map((item) => item.subject),
    ["/team", "iOS"],
  );
  // The scope chip is searchable alongside the subject.
  assert.deepEqual(
    filterAndSortBudgets(budgets, "label", "subject").map((item) => item.subject),
    ["iOS"],
  );
});

test("budgetSourceTitle explains manual and config sources", () => {
  assert.equal(budgetSourceTitle({ source: "manual" }), "Created from the dashboard.");
  assert.equal(budgetSourceTitle({ source: "config" }), "Loaded from configuration.");
  assert.equal(budgetSourceTitle({ source: "import" }), "Budget source: import");
});

test("budget period label, classes, icon, and duration distinguish standard and custom periods", () => {
  const matrix = [
    [3600, "Hourly", "hourly", "clock", "1 hour"],
    [86400, "Daily", "daily", "sun", "1 day"],
    [604800, "Weekly", "weekly", "calendar-days", "1 week"],
    [2592000, "Monthly", "monthly", "calendar", "1 month"],
  ];
  for (const [seconds, label, suffix, icon, duration] of matrix) {
    const item = { period_seconds: seconds };
    assert.equal(budgetPeriodLabel(item), label);
    assert.equal(budgetPeriodClass(item), "budget-period-label-" + suffix);
    assert.equal(budgetPeriodBarClass(item), "budget-bar-fill-period-" + suffix);
    assert.equal(budgetPeriodTrackClass(item), "budget-bar-track-period-" + suffix);
    assert.equal(budgetPeriodIcon(item), icon);
    assert.equal(budgetPeriodDurationLabel(item), duration);
  }

  assert.equal(
    budgetPeriodLabel({ period_seconds: 7200, period_label: "7200s" }),
    "Custom 7200s",
  );
  assert.equal(budgetPeriodClass({ period_seconds: 7200 }), "budget-period-label-custom");
  assert.equal(
    budgetPeriodBarClass({ period_seconds: 7200 }),
    "budget-bar-fill-period-custom",
  );
  assert.equal(
    budgetPeriodTrackClass({ period_seconds: 7200 }),
    "budget-bar-track-period-custom",
  );
  assert.equal(budgetPeriodIcon({ period_seconds: 7200 }), "settings-2");
  assert.equal(budgetPeriodDurationLabel({ period_seconds: 7200 }), "7200 seconds");
  assert.equal(budgetPeriodDurationLabel({ period_seconds: 1 }), "1 second");
  assert.equal(budgetPeriodFromSeconds(7200), "custom");
});

test("request bodies keep the exact /admin/budgets payload shapes", () => {
  const item = { scope: "user_path", subject: "/team", period_seconds: 86400 };
  assert.equal(
    JSON.stringify(budgetPutBody({ ...item, amount: 12.5 })),
    JSON.stringify({
      scope: "user_path",
      subject: "/team",
      budget_key: { period_seconds: 86400 },
      amount: 12.5,
    }),
  );
  assert.equal(
    JSON.stringify(budgetDeleteBody(item)),
    JSON.stringify({
      scope: "user_path",
      subject: "/team",
      budget_key: { period_seconds: 86400 },
    }),
  );
  assert.equal(
    JSON.stringify(budgetResetOneBody(item)),
    JSON.stringify({ scope: "user_path", subject: "/team", period_seconds: 86400 }),
  );
  assert.equal(
    JSON.stringify(
      budgetPutBody({ scope: "label", subject: "iOS", period_seconds: 86400, amount: 5 }),
    ),
    JSON.stringify({
      scope: "label",
      subject: "iOS",
      budget_key: { period_seconds: 86400 },
      amount: 5,
    }),
  );
});
