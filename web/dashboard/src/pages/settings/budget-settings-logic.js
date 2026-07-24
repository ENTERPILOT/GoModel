// Pure budget-settings logic.
// (The budgets page itself lives in src/pages/budgets/.)

export function defaultBudgetSettings() {
  return {
    daily_reset_hour: 0,
    daily_reset_minute: 0,
    weekly_reset_weekday: 1,
    weekly_reset_hour: 0,
    weekly_reset_minute: 0,
    monthly_reset_day: 1,
    monthly_reset_hour: 0,
    monthly_reset_minute: 0,
  };
}

// normalizeBudgetSettings coerces every reset anchor to an integer, falling
// back to the current in-editor value and then the defaults. Blank and
// fractional values fall back.
export function normalizeBudgetSettings(payload, current) {
  const existing = current || {};
  const integerValue = (value, fallback) => {
    if (value === "") {
      return fallback;
    }
    const parsed = Number(value);
    return Number.isFinite(parsed) && Number.isInteger(parsed)
      ? Math.trunc(parsed)
      : fallback;
  };
  const currentValue = (key, fallback) => integerValue(existing[key], fallback);
  const numberValue = (key, fallback) => {
    if (!payload) {
      return fallback;
    }
    return integerValue(payload[key], fallback);
  };
  return {
    daily_reset_hour: numberValue(
      "daily_reset_hour",
      currentValue("daily_reset_hour", 0),
    ),
    daily_reset_minute: numberValue(
      "daily_reset_minute",
      currentValue("daily_reset_minute", 0),
    ),
    weekly_reset_weekday: numberValue(
      "weekly_reset_weekday",
      currentValue("weekly_reset_weekday", 1),
    ),
    weekly_reset_hour: numberValue(
      "weekly_reset_hour",
      currentValue("weekly_reset_hour", 0),
    ),
    weekly_reset_minute: numberValue(
      "weekly_reset_minute",
      currentValue("weekly_reset_minute", 0),
    ),
    monthly_reset_day: numberValue(
      "monthly_reset_day",
      currentValue("monthly_reset_day", 1),
    ),
    monthly_reset_hour: numberValue(
      "monthly_reset_hour",
      currentValue("monthly_reset_hour", 0),
    ),
    monthly_reset_minute: numberValue(
      "monthly_reset_minute",
      currentValue("monthly_reset_minute", 0),
    ),
  };
}

export function budgetWeekdays() {
  return [
    { value: 0, label: "Sunday" },
    { value: 1, label: "Monday" },
    { value: 2, label: "Tuesday" },
    { value: 3, label: "Wednesday" },
    { value: 4, label: "Thursday" },
    { value: 5, label: "Friday" },
    { value: 6, label: "Saturday" },
  ];
}
