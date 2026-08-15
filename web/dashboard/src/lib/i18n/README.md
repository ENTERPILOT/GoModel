# Dashboard translations

Paraglide JS v2 reads JSON catalogs from `messages/`. English (`en`) is the
source, runtime default, and fallback locale. Change `baseLocale` only if
English stops being the source language; it is configured in
`project.inlang/settings.json`. Never edit generated files in `src/lib/paraglide/`.

## Using a message

```svelte
<script>
  import * as m from "$lib/paraglide/messages.js";
</script>

<label>{m.auth_api_key_label()}</label>
<p>{m.pagination_summary({ start: 1, end: 25, total: 80 })}</p>
```

Use flat `snake_case` keys. Reuse a message only when its meaning is identical,
and prefer complete phrases with named inputs so translators can reorder text.

## Adding a locale

1. Copy `messages/en.json` to a BCP 47 tag such as `pl.json` or `pt-BR.json`.
2. Translate values without changing keys, inputs, or plural branches.
3. Add the locale tag to `locales` in `project.inlang/settings.json`.
4. Run `npm test`, `npm run check`, and `npm run build`.

The locale selector in Settings reads this list automatically. Tests reject
unused English messages.
