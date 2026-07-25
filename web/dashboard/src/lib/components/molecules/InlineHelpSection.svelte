<script>
  // Title row with an inline-help "?" toggle and collapsible copy below the
  // row (inline-help-section class names match dashboard.css selectors).
  // The heading is the `title` snippet (h2/h3/label/p — whatever the page
  // needs). Copy comes from `text` (plain string) or the `help` snippet
  // (rich markup); the toggle is hidden when there is no copy. `external`
  // means the caller renders the copy somewhere else (e.g. another grid
  // cell): pair it with bind:open and give that copy element id={copyId}
  // so the aria wiring stays intact.
  let {
    copyId,
    // aria-label reads "Show {label}" / "Hide {label}".
    label = "help",
    text = "",
    open = $bindable(false),
    external = false,
    title,
    help,
    // Optional trailing row content after the toggle (e.g. a spinner).
    extra,
  } = $props();

  const hasHelp = $derived(Boolean(text) || Boolean(help) || external);
</script>

<div class="inline-help-section">
  <div class="inline-help-title-row">
    {@render title?.()}
    {#if hasHelp}
      <button
        type="button"
        class="inline-help-toggle"
        class:is-open={open}
        aria-label={(open ? "Hide " : "Show ") + label}
        aria-expanded={open}
        aria-controls={copyId}
        onclick={() => (open = !open)}
      >
        <span class="inline-help-toggle-icon" aria-hidden="true">?</span>
      </button>
    {/if}
    {@render extra?.()}
  </div>
  {#if open && hasHelp && !external}
    <p id={copyId} class="inline-help-copy">
      {#if help}{@render help()}{:else}{text}{/if}
    </p>
  {/if}
</div>

<style>
  .inline-help-toggle.is-open {
    color: var(--text);
    background: transparent;
  }

  .inline-help-toggle.is-open :global(.inline-help-toggle-icon) {
    transform: rotate(540deg);
  }
</style>
