// Exports the global Svelte 5 `i18n` service and its storage key. Components
// call i18n.t()/plural() directly; rune reads inside those methods keep
// rendered messages reactive.

import { readStored, writeStored } from "$lib/utils/storage.js";
import {
  localeDirection,
  matchLocale,
  pluralKey,
  translate,
} from "./catalog.js";
import { DEFAULT_LOCALE, DEFAULT_MESSAGES, LOCALES } from "./locales.js";

export const LOCALE_STORAGE_KEY = "gomodel_locale";

class I18nService {
  locale = $state(DEFAULT_LOCALE);
  messages = $state(DEFAULT_MESSAGES);
  loading = $state(false);
  initialized = false;
  loadSequence = 0;

  get availableLocales() {
    return Object.entries(LOCALES).map(([value, definition]) => ({
      value,
      label: definition.label,
    }));
  }

  get direction() {
    return localeDirection(this.locale);
  }

  init() {
    if (this.initialized) return;
    this.initialized = true;

    const browserLocales =
      typeof navigator === "undefined"
        ? []
        : navigator.languages || [navigator.language];
    const preferred = [readStored(LOCALE_STORAGE_KEY), ...browserLocales].filter(
      Boolean,
    );
    void this.setLocale(
      matchLocale(preferred, Object.keys(LOCALES), DEFAULT_LOCALE),
      { persist: false },
    );
  }

  async setLocale(requestedLocale, { persist = true } = {}) {
    const locale = matchLocale(
      [requestedLocale],
      Object.keys(LOCALES),
      DEFAULT_LOCALE,
    );
    const sequence = ++this.loadSequence;
    this.loading = true;

    try {
      const loaded = await LOCALES[locale].load();
      if (sequence !== this.loadSequence) return false;
      this.messages = loaded.default || loaded;
      this.locale = locale;
      if (persist) writeStored(LOCALE_STORAGE_KEY, locale);
      this.syncDocument();
      return true;
    } catch (error) {
      if (sequence !== this.loadSequence) return false;
      console.error(`Unable to load dashboard locale "${locale}".`, error);
      this.messages = DEFAULT_MESSAGES;
      this.locale = DEFAULT_LOCALE;
      this.syncDocument();
      return false;
    } finally {
      if (sequence === this.loadSequence) this.loading = false;
    }
  }

  t(key, values) {
    return translate(this.messages, DEFAULT_MESSAGES, key, values);
  }

  plural(key, count, values = {}) {
    const messageKey = pluralKey(
      this.locale,
      key,
      count,
      this.messages,
      DEFAULT_MESSAGES,
    );
    return this.t(messageKey, { ...values, count });
  }

  formatNumber(value, options) {
    return new Intl.NumberFormat(this.locale, options).format(value);
  }

  formatDate(value, options) {
    return new Intl.DateTimeFormat(this.locale, options).format(value);
  }

  syncDocument() {
    if (typeof document === "undefined") return;
    document.documentElement.lang = this.locale;
    document.documentElement.dir = this.direction;
  }
}

export const i18n = new I18nService();
