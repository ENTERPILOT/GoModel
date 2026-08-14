# Dashboard translations

The dashboard uses Paraglide JS v2. English in `messages/en.json` is the source
catalog, and `project.inlang/settings.json` declares the supported locales.
Paraglide compiles those JSON files into type-safe, tree-shakable functions in
`src/lib/paraglide/`; never edit that generated directory.

## Using a message

Import generated messages as a namespace and call the message directly:

```svelte
<script>
  import * as m from "$lib/paraglide/messages.js";
</script>

<label>{m.auth_api_key_label()}</label>
<p>{m.pagination_summary({ start: 1, end: 25, total: 80 })}</p>
<span>{m.date_picker_last_days({ count: days })}</span>
```

Use flat semantic keys such as `auth_api_key_invalid`. Reuse a message only
when its meaning is identical. Prefer complete phrases with named inputs over
concatenating translated fragments, so translators can change word order.

## Adding a locale

1. Copy `messages/en.json` to a BCP 47-named file such as `pl.json` or
   `pt-BR.json`.
2. Translate message values without changing keys or input names. Preserve
   declarations, selectors, and match branches used by plural messages.
3. Add the locale tag to `locales` in `project.inlang/settings.json`.
4. Run `npm run i18n:compile`, then `npm run check`, `npm test`, and
   `npm run build`.

The locale selector reads Paraglide's generated locale list and displays each
language in its own language, so no separate registry needs updating. Tests
also reject source-catalog messages that are not referenced by dashboard code.
