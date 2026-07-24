<script>
  // Global toast stack for the flash store. Mounted once in App.svelte;
  // pages call flash.success()/flash.error() instead of rendering their
  // own notice/error banners.
  import { fly, fade } from "svelte/transition";
  import { backOut } from "svelte/easing";
  import { flash } from "$lib/stores/flash.svelte.js";
</script>

<div class="flash-region">
  {#each flash.toasts as toast (toast.id)}
    <div
      class="flash-toast"
      class:flash-toast-success={toast.kind === "success"}
      class:flash-toast-error={toast.kind === "error"}
      role={toast.kind === "error" ? "alert" : "status"}
      aria-live={toast.kind === "error" ? "assertive" : "polite"}
      in:fly={{ y: 32, duration: 360, easing: backOut }}
      out:fade={{ duration: 150 }}
    >
      <span class="flash-toast-text">{toast.text}</span>
      <button
        type="button"
        class="flash-toast-dismiss"
        aria-label="Dismiss notification"
        onclick={() => flash.dismiss(toast.id)}
      >
        &times;
      </button>
    </div>
  {/each}
</div>

<style>
  .flash-region {
    position: fixed;
    bottom: 16px;
    right: 16px;
    /* Above modal shells (z 90) so feedback fired from a dialog is seen. */
    z-index: 120;
    display: flex;
    flex-direction: column;
    gap: 10px;
    width: min(360px, calc(100vw - 32px));
    pointer-events: none;
  }

  .flash-toast {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px 12px 12px 16px;
    border-radius: var(--radius);
    font-size: 14px;
    box-shadow: 0 10px 30px rgba(0, 0, 0, 0.35);
    pointer-events: auto;
    /* Entry attention pulse: a colored glow (currentColor = the kind's
       accent) that fades out, layered on the fly-in slide. */
    animation: flash-toast-glow 900ms ease-out;
  }

  @keyframes flash-toast-glow {
    0% {
      box-shadow:
        0 0 0 4px color-mix(in srgb, currentColor 45%, transparent),
        0 10px 30px rgba(0, 0, 0, 0.35);
    }
    100% {
      box-shadow:
        0 0 0 4px transparent,
        0 10px 30px rgba(0, 0, 0, 0.35);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .flash-toast {
      animation: none;
    }
  }

  .flash-toast-success {
    background: color-mix(in srgb, var(--success) 14%, var(--bg-surface));
    border: 1px solid rgba(52, 211, 153, 0.35);
    color: var(--success);
  }

  .flash-toast-error {
    background: color-mix(in srgb, var(--warning) 14%, var(--bg-surface));
    border: 1px solid rgba(245, 158, 11, 0.4);
    color: var(--warning);
  }

  .flash-toast-text {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .flash-toast-dismiss {
    flex-shrink: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font-size: 18px;
    line-height: 1;
    padding: 0 2px;
    cursor: pointer;
    opacity: 0.7;
  }

  .flash-toast-dismiss:hover {
    opacity: 1;
  }
</style>
