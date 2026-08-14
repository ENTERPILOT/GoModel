<script>
  // Locale picker. It deliberately renders nothing until a second locale is
  // registered, so adding a translation is the only step needed to expose it.
  import { i18n } from "$lib/i18n/i18n.svelte.js";

  let { compact = false } = $props();
</script>

{#if i18n.availableLocales.length > 1}
  <label class="locale-selector" class:is-compact={compact}>
    <span>{i18n.t("language.label")}</span>
    <select
      value={i18n.locale}
      aria-label={i18n.t("language.label")}
      disabled={i18n.loading}
      onchange={(event) => i18n.setLocale(event.currentTarget.value)}
    >
      {#each i18n.availableLocales as locale (locale.value)}
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
