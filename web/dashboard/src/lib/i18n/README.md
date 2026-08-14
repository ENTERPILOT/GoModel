# Dashboard translations

The dashboard uses a small Svelte 5 rune service and the browser's `Intl`
implementation. There is no runtime i18n dependency. English in
`locales/en.js` is the source catalog and the fallback when a translation is
still incomplete.

## Using a message

Import the singleton and use a semantic key:

```svelte
<script>
  import { i18n } from "$lib/i18n/i18n.svelte.js";
</script>

<label>{i18n.t("auth.apiKey.label")}</label>
<p>{i18n.t("pagination.summary", { start: 1, end: 25, total: 80 })}</p>
<span>{i18n.plural("datePicker.lastDays", days)}</span>
```

Name keys by meaning (`auth.apiKey.invalid`), not by their English text or
screen position. Reuse a key only when its meaning is identical. Prefer a
complete sentence with named placeholders over concatenating translated
fragments; translators must be able to change word order.

## Adding a locale

1. Copy `locales/en.js` to a BCP 47-named file such as `pl.js` or `pt-BR.js`.
2. Translate values without changing keys or placeholder names. Use the
   language's own name for its registry label (for example, `Polski`).
3. Add the locale to `LOCALES` in `locales.js` with a dynamic import, for
   example `load: () => import("./locales/pl.js")`.
4. Run `npm run check`, `npm test`, and `npm run build`. Catalog tests reject
   unknown keys, blank values, and placeholder mismatches automatically.

Missing entries intentionally fall back to English, so contributors can land
translations incrementally. Keep source values explicit in each locale file;
do not import and spread English into a translation, because that hides
missing work from reviewers and catalog validation.
