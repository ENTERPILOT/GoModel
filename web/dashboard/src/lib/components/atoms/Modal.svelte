<script>
  // Overlay dialog whose backdrop/shell class names match dashboard.css
  // selectors. Handles Escape, backdrop click, body scroll
  // lock (via the modals store), and autofocus of [data-modal-autofocus].
  // Children render inside the shell; give the top-level child the dialog
  // role/class (e.g. <section class="model-editor" role="dialog">).
  import { tick, untrack } from "svelte";
  import { modals } from "$lib/stores/ui.svelte.js";

  let {
    open = false,
    onclose,
    // Class pair: "editor" (editor-modal-*) or "auth" (auth-dialog-*).
    variant = "editor",
    closeOnBackdrop = true,
    children,
  } = $props();

  const backdropClass = $derived(
    variant === "auth" ? "auth-dialog-backdrop" : "editor-modal-backdrop",
  );
  const shellClass = $derived(
    variant === "auth" ? "auth-dialog-shell" : "editor-modal-shell",
  );

  let shellEl = $state(null);

  $effect(() => {
    if (!open) return;
    // untrack: opened() reads AND writes modals.stack; tracking that read
    // would make the effect invalidate itself and loop (effect_update_depth).
    const token = untrack(() => modals.opened());
    tick().then(() => {
      const target = shellEl && shellEl.querySelector("[data-modal-autofocus]");
      if (target && typeof target.focus === "function") {
        target.focus();
      }
    });
    const onKeydown = (event) => {
      // Only the topmost dialog reacts to Escape (stacked dialogs, e.g. the
      // auth dialog over an editor, must not both close).
      if (event.key === "Escape" && modals.isTop(token)) {
        onclose?.();
      }
    };
    window.addEventListener("keydown", onKeydown);
    return () => {
      modals.closed(token);
      window.removeEventListener("keydown", onKeydown);
    };
  });

  function onShellClick(event) {
    if (closeOnBackdrop && event.target === shellEl) {
      onclose?.();
    }
  }
</script>

{#if open}
  <div class={backdropClass} aria-hidden="true"></div>
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class={shellClass} bind:this={shellEl} onclick={onShellClick}>
    {@render children?.()}
  </div>
{/if}
