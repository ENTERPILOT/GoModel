<script>
  // Announces a newer GoModel release at the top of the Settings page.
  //
  // The dismiss control hides it for this page session only (see
  // versionStore.dismissed): an update stays available until it is installed,
  // so the notice returns on the next load rather than being silenced for good.
  import { versionStore } from "$lib/stores/version.svelte.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { ArrowUpCircle, X } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

{#if versionStore.noticeVisible}
  <aside
    class="update-banner"
    role="status"
    aria-label={m.update_banner_label()}
  >
    <Icon icon={ArrowUpCircle} class="update-banner-icon" />
    <p class="update-banner-copy">
      {m.update_banner_message({
        app: versionStore.app,
        latest: versionStore.latest,
        current: versionStore.current,
      })}
    </p>
    {#if versionStore.releaseNotesURL}
      <a
        href={versionStore.releaseNotesURL}
        target="_blank"
        rel="noopener noreferrer">{m.update_banner_link()}</a
      >
    {/if}
    <button
      type="button"
      class="update-banner-dismiss"
      onclick={() => versionStore.dismissNotice()}
      aria-label={m.update_banner_dismiss()}
      title={m.update_banner_dismiss()}
    >
      <Icon icon={X} class="update-banner-dismiss-icon" />
    </button>
  </aside>
{/if}

<style>
  .update-banner {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 20px;
    padding: 10px 14px;
    border: 1px solid color-mix(in srgb, var(--accent) 55%, var(--border));
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--accent) 12%, var(--bg-surface));
    color: var(--text);
    font-size: 13px;
  }

  .update-banner :global(.update-banner-icon) {
    width: 18px;
    height: 18px;
    flex: 0 0 18px;
    color: var(--accent);
  }

  .update-banner-copy {
    flex: 1;
    min-width: 0;
    margin: 0;
  }

  .update-banner a {
    flex-shrink: 0;
    color: var(--accent);
    font-weight: 600;
    text-decoration: none;
    white-space: nowrap;
  }

  .update-banner a:hover {
    text-decoration: underline;
  }

  .update-banner-dismiss {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 24px;
    height: 24px;
    padding: 0;
    border: 1px solid transparent;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }

  .update-banner-dismiss:hover {
    border-color: color-mix(in srgb, var(--accent) 42%, var(--border));
    color: var(--text);
  }

  .update-banner :global(.update-banner-dismiss-icon) {
    width: 14px;
    height: 14px;
  }
</style>
