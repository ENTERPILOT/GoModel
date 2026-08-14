<script>
  // Settings locale picker. It deliberately renders nothing until a second
  // locale is configured, so adding a translation is enough to expose it.
  import { localeDisplayName } from "$lib/i18n/locale.js";
  import * as m from "$lib/paraglide/messages.js";
  import { getLocale, locales, setLocale } from "$lib/paraglide/runtime.js";

  const availableLocales = locales.map((value) => ({
    value,
    label: localeDisplayName(value),
  }));
</script>

{#if availableLocales.length > 1}
  <div class="settings-refresh-section locale-settings">
    <label class="locale-selector-label" for="dashboard-locale">
      {m.language_label()}
    </label>
    <select
      id="dashboard-locale"
      class="form-select settings-select"
      value={getLocale()}
      aria-label={m.language_label()}
      onchange={(event) => setLocale(event.currentTarget.value)}
    >
      {#each availableLocales as locale (locale.value)}
        <option value={locale.value}>{locale.label}</option>
      {/each}
    </select>
  </div>
{/if}

<style>
  .locale-settings {
    grid-template-columns: minmax(280px, 420px);
  }

  .locale-selector-label {
    color: var(--text);
    font-size: 18px;
    font-weight: 600;
  }
</style>
