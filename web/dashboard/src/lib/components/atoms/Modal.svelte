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

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .auth-dialog-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.48);
    z-index: 80;
  }

  .editor-modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.48);
    z-index: 80;
  }

  .auth-dialog-shell {
    position: fixed;
    inset: 0;
    z-index: 90;
    display: grid;
    place-items: center;
    padding: 20px;
  }

  .editor-modal-shell {
    position: fixed;
    inset: 0;
    z-index: 90;
    display: grid;
    place-items: center;
    padding: 20px;
    overflow-y: auto;
  }

  .editor-modal-shell > :global(*) {
    width: min(760px, 100%);
    max-height: min(calc(100vh - 40px), 960px);
    margin: 0;
    overflow: auto;
    overscroll-behavior: contain;
    box-shadow: 0 24px 70px rgba(0, 0, 0, 0.38);
  }

  @media (max-width: 768px) {
    .auth-dialog-shell {
        align-items: end;
        padding: 12px;
      }

    .editor-modal-shell {
        align-items: end;
        padding: 12px;
      }

    .editor-modal-shell > :global(*) {
        max-height: calc(100vh - 24px);
      }
  }
</style>
