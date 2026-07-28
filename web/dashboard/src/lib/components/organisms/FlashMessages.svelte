<script>
  // Global toast stack for the flash store. Mounted once in App.svelte;
  // pages call flash.success()/flash.error() instead of rendering their
  // own notice/error banners.
  import { fly, fade } from "svelte/transition";
  import { backOut } from "svelte/easing";
  import { flash } from "$lib/stores/flash.svelte.ts";
</script>

<div class="flash-region">
  {#each flash.toasts as toast (toast.id)}
    <div
      class="flash-toast"
      class:flash-toast-success={toast.kind === "success"}
      class:flash-toast-error={toast.kind === "error"}
      role={toast.kind === "error" ? "alert" : "status"}
      aria-live={toast.kind === "error" ? "assertive" : "polite"}
      in:fly={{ y: -24, duration: 360, easing: backOut }}
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
  /* Anchored top-center over the main content area: spans from the
     sidebar's right edge to the viewport edge, toasts centered within. */
  .flash-region {
    position: fixed;
    top: 16px;
    left: var(--sidebar-width);
    right: 0;
    /* Above modal shells (z 90) so feedback fired from a dialog is seen. */
    z-index: 120;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    pointer-events: none;
  }

  /* Track the sidebar's collapsed (60px) width; same on mobile. */
  :global(.sidebar.sidebar-collapsed) ~ .flash-region {
    left: 60px;
  }

  @media (max-width: 768px) {
    .flash-region {
      left: 60px;
    }
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
    width: max-content;
    max-width: min(480px, calc(100% - 32px));
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
