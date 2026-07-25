<script>
  // Copy-to-clipboard button with inline feedback. The caller owns the state
  // (from createCopyState) and performs the copy in `onclick`, so the same
  // button works for page-local state and store-held state.
  import Icon from "$lib/components/atoms/Icon.svelte";

  let {
    state,
    label = "Copy",
    copiedLabel = "Copied",
    // Left unset when the caller reports failures elsewhere (e.g. a form-error
    // line), so the label keeps reading "Copy".
    errorLabel = "",
    onclick,
    class: className = "pagination-btn",
  } = $props();

  const text = $derived(
    state.error && errorLabel ? errorLabel : state.copied ? copiedLabel : label,
  );
</script>

<button
  type="button"
  class="copy-feedback-btn {className}"
  class:copy-feedback-btn-copied={state.copied}
  onclick={(event) => {
    event.preventDefault();
    onclick?.(event);
  }}
>
  {#if state.copied}
    <Icon name="circle-check" width="14" height="14" stroke-width="2.5" />
  {:else}
    <Icon name="copy" width="14" height="14" />
  {/if}
  <span aria-live="polite" aria-atomic="true">{text}</span>
</button>
