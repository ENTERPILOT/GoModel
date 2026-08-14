<script>
  // Locale picker. It deliberately renders nothing until a second locale is
  // configured, so adding a translation is the only step needed to expose it.
  import { localeDisplayName } from "$lib/i18n/locale.js";
  import * as m from "$lib/paraglide/messages.js";
  import { getLocale, locales, setLocale } from "$lib/paraglide/runtime.js";

  let { compact = false } = $props();

  const availableLocales = locales.map((value) => ({
    value,
    label: localeDisplayName(value),
  }));
</script>

{#if availableLocales.length > 1}
  <label class="locale-selector" class:is-compact={compact}>
    <span>{m.language_label()}</span>
    <select
      value={getLocale()}
      aria-label={m.language_label()}
      onchange={(event) => setLocale(event.currentTarget.value)}
    >
      {#each availableLocales as locale (locale.value)}
        <option value={locale.value}>{locale.label}</option>
      {/each}
    </select>
  </label>
{/if}

<style>
  .locale-selector {
    display: grid;
    gap: 5px;
    margin-bottom: 10px;
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 600;
  }

  .locale-selector select {
    min-width: 0;
    width: 100%;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    color: var(--text);
    font: inherit;
  }

  .locale-selector.is-compact {
    display: none;
  }
</style>
