<script>
  // Theme picker. Two presentations of the same control: a three-way pill
  // when there is room, and a single cycling button when there isn't.
  // `compact` forces the narrow form regardless of viewport (collapsed
  // sidebar); below 768px it is used regardless.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { themeStore } from "$lib/stores/ui.svelte.js";

  let { compact = false } = $props();

  const themes = [
    { value: "light", icon: "sun", label: "Light theme" },
    { value: "system", icon: "monitor", label: "System theme" },
    { value: "dark", icon: "moon", label: "Dark theme" },
  ];
  const activeTheme = $derived(
    themes.find((t) => t.value === themeStore.theme) || themes[1],
  );
  // The narrow button cycles rather than selects, so it is named for the
  // action it performs and carries the current theme as context.
  const cycleLabel = $derived("Change theme (currently " + activeTheme.label + ")");
</script>

<div class="theme-toggle" class:is-compact={compact} role="group" aria-label="Theme">
  {#each themes as theme (theme.value)}
    <button
      class="theme-btn"
      class:active={themeStore.theme === theme.value}
      aria-pressed={themeStore.theme === theme.value}
      onclick={() => themeStore.set(theme.value)}
      title={theme.label}
      aria-label={theme.label}
    >
      <Icon name={theme.icon} class="theme-icon" />
    </button>
  {/each}
</div>
<button
  class="theme-toggle-mobile"
  class:is-compact={compact}
  onclick={() => themeStore.toggle()}
  title={cycleLabel}
  aria-label={cycleLabel}
>
  <Icon name={activeTheme.icon} class="theme-icon" />
</button>

<style>
  /* Wide form: a compact pill of three segments. */
  .theme-toggle {
    display: inline-flex;
    align-items: center;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 2px;
    margin-bottom: 10px;
  }

  .theme-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 24px;
    background: transparent;
    border: none;
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 0.15s;
  }

  .theme-btn:hover {
    color: var(--text);
  }

  .theme-btn.active {
    background: var(--accent);
    color: #fff;
  }

  .theme-btn:focus-visible,
  .theme-toggle-mobile:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 36%, transparent);
    outline-offset: 2px;
  }

  /* Narrow form: one button that cycles light -> system -> dark. */
  .theme-toggle-mobile {
    display: none;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 0.15s;
  }

  .theme-toggle-mobile:hover {
    color: var(--text);
  }

  /* Swap the two forms. The caller's `compact` and the viewport breakpoint
     express the same intent, so they carry the same pair of rules. */
  .theme-toggle.is-compact {
    display: none;
  }

  .theme-toggle-mobile.is-compact {
    display: flex;
    margin: 0 auto;
  }

  @media (max-width: 768px) {
    .theme-toggle {
        display: none;
      }

    .theme-toggle-mobile {
        display: flex;
        margin: 0 auto;
      }
  }
</style>
