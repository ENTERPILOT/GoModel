// Exports the locale registry plus English default locale/messages. English
// stays eagerly available as the fallback; other locales should be lazy-loaded.

import en from "./locales/en.js";

export const DEFAULT_LOCALE = "en";
export const DEFAULT_MESSAGES = en;

export const LOCALES = Object.freeze({
  en: {
    label: "English",
    load: async () => en,
  },
});
