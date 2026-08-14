// Locale helpers around Paraglide's generated runtime. Message lookup stays in
// generated functions; this module owns browser document state and Intl usage.

import {
  defineCustomClientStrategy,
  getLocale,
  getTextDirection,
} from "../paraglide/runtime.js";
import { readStored, writeStored } from "../utils/storage.js";

const LOCALE_STORAGE_KEY = "gomodel_locale";

defineCustomClientStrategy("custom-dashboard", {
  getLocale: () => readStored(LOCALE_STORAGE_KEY) || undefined,
  setLocale: (locale) => writeStored(LOCALE_STORAGE_KEY, locale),
});

export function syncDocumentLocale() {
  const locale = getLocale();
  document.documentElement.lang = locale;
  document.documentElement.dir = getTextDirection(locale);
}

export function localeDisplayName(locale) {
  if (typeof Intl.DisplayNames !== "function") return locale;
  return new Intl.DisplayNames([locale], { type: "language" }).of(locale) || locale;
}

export function formatNumber(value, options) {
  return new Intl.NumberFormat(getLocale(), options).format(value);
}

export function formatDate(value, options) {
  return new Intl.DateTimeFormat(getLocale(), options).format(value);
}
