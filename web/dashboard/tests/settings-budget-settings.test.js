// Ported from the budget-settings cases of
// internal/admin/dashboard/static/js/modules/budgets.test.cjs (the budgets
// page portion is tested with the budgets page).
import test from "node:test";
import assert from "node:assert/strict";

import {
  defaultBudgetSettings,
  normalizeBudgetSettings,
} from "../src/pages/settings/budget-settings-logic.js";

test("normalizeBudgetSettings normalizes numeric values before saving", () => {
  const current = {
    daily_reset_hour: "7",
    daily_reset_minute: "15",
    weekly_reset_weekday: "3",
    weekly_reset_hour: "8",
    weekly_reset_minute: "45",
    monthly_reset_day: "31",
    monthly_reset_hour: "9",
    monthly_reset_minute: "30",
  };

  assert.equal(
    JSON.stringify(normalizeBudgetSettings(current, current)),
    JSON.stringify({
      daily_reset_hour: 7,
      daily_reset_minute: 15,
      weekly_reset_weekday: 3,
      weekly_reset_hour: 8,
      weekly_reset_minute: 45,
      monthly_reset_day: 31,
      monthly_reset_hour: 9,
      monthly_reset_minute: 30,
    }),
  );
});

test("normalizeBudgetSettings falls back for blank and fractional values", () => {
  const current = {
    daily_reset_hour: "",
    daily_reset_minute: "15.5",
    weekly_reset_weekday: "3",
    weekly_reset_hour: "8",
    weekly_reset_minute: "45",
    monthly_reset_day: "31",
    monthly_reset_hour: "9",
    monthly_reset_minute: "30",
  };

  const payload = normalizeBudgetSettings(current, current);
  assert.equal(payload.daily_reset_hour, 0);
  assert.equal(payload.daily_reset_minute, 0);
  assert.equal(payload.weekly_reset_weekday, 3);
});

test("normalizeBudgetSettings keeps in-editor values when the payload is missing fields", () => {
  const current = { ...defaultBudgetSettings(), monthly_reset_day: 15 };
  const normalized = normalizeBudgetSettings({ daily_reset_hour: 6 }, current);
  assert.equal(normalized.daily_reset_hour, 6);
  assert.equal(normalized.monthly_reset_day, 15);
  assert.equal(normalized.weekly_reset_weekday, 1);
});
