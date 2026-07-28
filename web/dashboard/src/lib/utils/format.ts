// splitCommaList turns a comma-separated input into a trimmed array,
// dropping empty items.
export function splitCommaList(value: unknown): string[] {
  return String(value || "")
    .split(",")
    .map((item) => item.trim())
    .filter((item) => item);
}

// Shared display formatters.

export function formatNumber(n: number | null | undefined): string {
  if (n == null || n === undefined) return "-";
  return n.toLocaleString();
}

export function formatCost(v: number | string | null | undefined): string {
  if (v == null) return "---";
  const cost = Number(v);
  if (!Number.isFinite(cost)) return "---";
  if (cost > 0 && cost < 0.0001) return "<$0.0001";
  return "$" + cost.toFixed(4).replace(/(\.\d{2}\d*?)0+$/, "$1");
}

export function formatPrice(v: number | null | undefined): string {
  if (v == null || v === undefined) return "—";
  return "$" + v.toFixed(2);
}

export function formatPriceFine(v: number | null | undefined): string {
  if (v == null || v === undefined) return "—";
  if (v < 0.01) return "$" + v.toFixed(6);
  return "$" + v.toFixed(4);
}

export function formatTokensShort(n: number | string | null | undefined): string {
  if (n == null || n === "") return "-";
  const value = Number(n);
  if (!Number.isFinite(value)) return "-";
  const absolute = Math.abs(value);
  const units = [
    { threshold: 1000000000, suffix: "B" },
    { threshold: 1000000, suffix: "M" },
    { threshold: 1000, suffix: "K" },
  ];
  for (let index = 0; index < units.length; index += 1) {
    let unit = units[index];
    if (absolute >= unit.threshold) {
      let compact = value / unit.threshold;
      if (Math.abs(Number(compact.toFixed(1))) >= 1000 && index > 0) {
        unit = units[index - 1];
        compact = value / unit.threshold;
      }
      return compact.toFixed(1).replace(/\.0$/, "") + unit.suffix;
    }
  }
  return String(value);
}

export function tokenCountTitle(
  label: string,
  n: number | string | null | undefined,
): string {
  const value = n == null || n === "" ? NaN : Number(n);
  const exact = Number.isFinite(value) ? formatNumber(value) : "-";
  return String(label || "Tokens") + ": " + exact;
}

// formatDateParam renders a Date (or passes a string through) as the UTC
// yyyy-mm-dd used in admin query parameters.
export function formatDateParam(date: Date | string | null | undefined): string {
  if (!date) return "";
  if (typeof date === "string") return date;
  return (
    date.getUTCFullYear() +
    "-" +
    String(date.getUTCMonth() + 1).padStart(2, "0") +
    "-" +
    String(date.getUTCDate()).padStart(2, "0")
  );
}

export function formatDateUTC(ts: string | number | Date | null | undefined): string {
  if (!ts) return "-";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "-";
  return (
    d.getUTCFullYear() +
    "-" +
    String(d.getUTCMonth() + 1).padStart(2, "0") +
    "-" +
    String(d.getUTCDate()).padStart(2, "0")
  );
}

export function formatTimestampUTC(ts: string | number | Date | null | undefined): string {
  if (!ts) return "-";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "-";
  return (
    d.getUTCFullYear() +
    "-" +
    String(d.getUTCMonth() + 1).padStart(2, "0") +
    "-" +
    String(d.getUTCDate()).padStart(2, "0") +
    " " +
    String(d.getUTCHours()).padStart(2, "0") +
    ":" +
    String(d.getUTCMinutes()).padStart(2, "0") +
    ":" +
    String(d.getUTCSeconds()).padStart(2, "0") +
    " UTC"
  );
}

// A value carrying "provider/model" naming fields, as found on usage rows,
// audit entries, and catalog entries. All fields optional: helpers render
// whatever is present.
export interface ProviderModelRef {
  provider?: string | null;
  provider_name?: string | null;
  model?: string | null;
  resolved_model?: string | null;
}

// providerTypeValue / providerDisplayValue / qualified* render "provider/model"
// pairs consistently across tables, pills, and dropdowns.
export function providerTypeValue(value: ProviderModelRef | null | undefined): string {
  return String((value && value.provider) || "").trim();
}

export function providerDisplayValue(value: ProviderModelRef | null | undefined): string {
  const providerName = String((value && value.provider_name) || "").trim();
  if (providerName) return providerName;
  return providerTypeValue(value);
}

export function qualifiedModelValueDisplay(
  value: ProviderModelRef | null | undefined,
  modelValue: string | null | undefined,
): string {
  const model = String(modelValue || "").trim();
  if (!model) return "-";
  const provider = providerDisplayValue(value);
  if (!provider || model === provider || model.startsWith(provider + "/"))
    return model;
  return provider + "/" + model;
}

export function qualifiedModelDisplay(value: ProviderModelRef | null | undefined): string {
  return qualifiedModelValueDisplay(value, value && value.model);
}

export function qualifiedResolvedModelDisplay(
  value: ProviderModelRef | null | undefined,
): string {
  return qualifiedModelValueDisplay(value, value && value.resolved_model);
}

// The audit-entry fields auditModelDisplay reads; the real entries carry more.
export interface AuditEntryModelRef extends ProviderModelRef {
  requested_model?: string | null;
  alias_used?: boolean;
  data?: { failover?: { target_model?: string | null } | null } | null;
}

// auditModelDisplay renders the audit summary pill. When the request was
// redirected — a runtime failover or a redirect/alias — it shows
// "requested ⮕ target"; otherwise a single value, so direct calls stay
// unchanged.
export function auditModelDisplay(entry: AuditEntryModelRef | null | undefined): string {
  const requested = String(
    (entry && (entry.requested_model || entry.model)) || "",
  ).trim();
  if (!entry) {
    return requested;
  }
  const failoverTarget = String(
    (entry.data && entry.data.failover && entry.data.failover.target_model) ||
      "",
  ).trim();
  if (failoverTarget && failoverTarget !== requested) {
    return requested + " ⮕ " + failoverTarget;
  }
  if (entry.alias_used && entry.resolved_model) {
    const resolved = qualifiedResolvedModelDisplay(entry);
    if (resolved && resolved !== "-" && resolved !== requested) {
      return requested + " ⮕ " + resolved;
    }
  }
  return requested;
}
