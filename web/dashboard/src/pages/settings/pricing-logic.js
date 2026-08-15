// Pure pricing-recalculation logic.

import { formatDateParam } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";

export { formatDateParam };

// pricingRecalculateDatePayload renders the shared date-range window as the
// recalculation request's date fields: preset -> {days}, custom range ->
// {start_date, end_date} with today's date as the end fallback.
export function pricingRecalculateDatePayload(range) {
  const window = range || {};
  if (window.selectedPreset) {
    return { days: parseInt(window.selectedPreset, 10) || 30 };
  }
  const start = window.customStartDate
    ? formatDateParam(window.customStartDate)
    : "";
  const endDate = window.customEndDate || window.today || null;
  const end = endDate ? formatDateParam(endDate) : "";
  return {
    start_date: start,
    end_date: end,
  };
}

export function pricingRecalculatePayload(range, userPath, selector, confirmation) {
  return {
    ...pricingRecalculateDatePayload(range),
    user_path: String(userPath || "").trim(),
    selector: String(selector || "").trim(),
    confirmation,
  };
}

export function pricingRecalculateSummary(result) {
  const matched = Number((result && result.matched) || 0);
  const recalculated = Number((result && result.recalculated) || 0);
  const withoutPricing = Number((result && result.without_pricing) || 0);
  let message = m.settings_pricing_summary({ matched, recalculated });
  if (withoutPricing > 0) {
    message += " " + m.settings_pricing_missing({ count: withoutPricing });
  }
  return message;
}
