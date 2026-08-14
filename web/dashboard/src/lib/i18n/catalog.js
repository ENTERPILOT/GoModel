// Rune-free i18n primitives: locale matching/direction, interpolation,
// translation/plural lookup, and catalog validation. They are shared by the
// Svelte service and the plain node:test suite.

/** Return a stable, canonical locale tag, or an empty string when invalid. */
export function canonicalLocale(locale) {
  try {
    return Intl.getCanonicalLocales(String(locale || ""))[0] || "";
  } catch {
    return "";
  }
}

/**
 * Pick the best supported locale from a browser-style preference list.
 * Exact tags win, then their base language, then the configured fallback.
 */
export function matchLocale(preferred, supported, fallback) {
  const available = new Map(
    supported
      .map((locale) => [canonicalLocale(locale), locale])
      .filter(([locale]) => locale),
  );
  const requested = Array.isArray(preferred) ? preferred : [preferred];

  for (const preference of requested) {
    const canonical = canonicalLocale(preference);
    if (!canonical) continue;
    if (available.has(canonical)) return available.get(canonical);

    const language = new Intl.Locale(canonical).language;
    for (const [candidate, original] of available) {
      if (new Intl.Locale(candidate).language === language) return original;
    }
  }

  return available.get(canonicalLocale(fallback)) || supported[0] || fallback;
}

/** Replace named `{placeholders}` without evaluating translation content. */
export function interpolate(message, values = {}) {
  return String(message).replace(/\{([A-Za-z][A-Za-z0-9_]*)\}/g, (token, name) =>
    Object.prototype.hasOwnProperty.call(values, name) ? String(values[name]) : token,
  );
}

/** Named placeholders used by a message, sorted for stable validation. */
export function messagePlaceholders(message) {
  return [...String(message).matchAll(/\{([A-Za-z][A-Za-z0-9_]*)\}/g)]
    .map((match) => match[1])
    .filter((name, index, all) => all.indexOf(name) === index)
    .sort();
}

/**
 * Validate a translated catalog against its source. Missing messages are
 * allowed because runtime fallback is intentional; unknown keys, blanks, and
 * changed placeholders are contributor errors.
 */
export function catalogErrors(messages, sourceMessages) {
  const errors = [];
  for (const [key, message] of Object.entries(messages)) {
    if (sourceMessages[key] == null) {
      errors.push(`${key}: key is not present in the source catalog`);
      continue;
    }
    if (typeof message !== "string" || message.trim() === "") {
      errors.push(`${key}: translation must be a non-empty string`);
      continue;
    }
    const expected = messagePlaceholders(sourceMessages[key]);
    const actual = messagePlaceholders(message);
    if (expected.join("\0") !== actual.join("\0")) {
      errors.push(`${key}: placeholders must be {${expected.join("}, {")}}`);
    }
  }
  return errors;
}

/** Resolve a translated message with the source catalog as the fallback. */
export function translate(messages, fallbackMessages, key, values) {
  const message = messages[key] ?? fallbackMessages[key];
  return message == null ? key : interpolate(message, values);
}

/** Choose a CLDR plural branch such as `.one`, `.few`, or `.other`. */
export function pluralKey(locale, key, count, messages, fallbackMessages) {
  const category = new Intl.PluralRules(locale).select(Number(count));
  const candidate = key + "." + category;
  if (messages[candidate] != null || fallbackMessages[candidate] != null) {
    return candidate;
  }
  return key + ".other";
}

/** Text direction for the writing systems currently used by RTL locales. */
export function localeDirection(locale) {
  const language = new Intl.Locale(canonicalLocale(locale) || "en").language;
  return [
    "ar",
    "arc",
    "ckb",
    "dv",
    "fa",
    "he",
    "ks",
    "ku",
    "nqo",
    "ps",
    "sd",
    "syr",
    "ug",
    "ur",
    "yi",
  ].includes(language)
    ? "rtl"
    : "ltr";
}
