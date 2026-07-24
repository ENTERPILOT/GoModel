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
  budgetSourceTitle,
  budgetUserPathValidationError,
  buildBudgetFormPayload,
  defaultBudgetForm,
  filterAndSortBudgets,
  findExistingBudget,
  normalizeBudgetListPayload,
  normalizeBudgetUserPath,
} from "../src/pages/budgets/budgets-helpers.js";

test("buildBudgetFormPayload normalizes user path and standard periods", () => {
  const { payload, error } = buildBudgetFormPayload({
    user_path: "team/alpha",
    period: "weekly",
    period_seconds: 0,
    amount: "12.3456",
    source: "",
  });

  assert.equal(error, "");
  assert.equal(
    JSON.stringify(payload),
    JSON.stringify({
      user_path: "/team/alpha",
      period_seconds: 604800,
      amount: 12.3456,
      source: "manual",
    }),
  );
});

test("buildBudgetFormPayload rejects invalid amounts and custom seconds", () => {
  const emptyAmount = buildBudgetFormPayload({
    ...defaultBudgetForm(),
    amount: "",
  });
  assert.equal(emptyAmount.payload, null);
  assert.equal(emptyAmount.error, "Amount must be greater than 0.");

  const badSeconds = buildBudgetFormPayload({
    user_path: "/team",
    period: "custom",
    period_seconds: 0,
    amount: "5",
    source: "manual",
  });
  assert.equal(badSeconds.payload, null);
  assert.equal(badSeconds.error, "Period seconds must be greater than 0.");

  const badPath = buildBudgetFormPayload({
    ...defaultBudgetForm(),
    user_path: "/team/../oops",
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
  assert.equal(defaultBudgetForm().user_path, "/");
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

test("findExistingBudget matches on the user path + period key", () => {
  const budgets = [
    { user_path: "/team", period_seconds: 86400, amount: 10 },
    { user_path: "/team", period_seconds: 3600, amount: 2 },
  ];
  const payload = { user_path: "/team", period_seconds: 86400, amount: 12.5 };

  assert.equal(budgetKey(payload), "/team:86400");
  assert.equal(findExistingBudget(budgets, payload), budgets[0]);
  assert.equal(
    findExistingBudget(budgets, { user_path: "/other", period_seconds: 86400 }),
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
  assert.equal(
    JSON.stringify(
      budgetPutBody({ user_path: "/team", period_seconds: 86400, amount: 12.5 }),
    ),
    JSON.stringify({
      user_path: "/team",
      budget_key: { period_seconds: 86400 },
      amount: 12.5,
    }),
  );
  assert.equal(
    JSON.stringify(budgetDeleteBody({ user_path: "/team", period_seconds: 86400 })),
    JSON.stringify({
      user_path: "/team",
      budget_key: { period_seconds: 86400 },
    }),
  );
  assert.equal(
    JSON.stringify(budgetResetOneBody({ user_path: "/team", period_seconds: 86400 })),
    JSON.stringify({ user_path: "/team", period_seconds: 86400 }),
  );
});
