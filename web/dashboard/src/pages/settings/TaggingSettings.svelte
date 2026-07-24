<script>
  // Tagging-based-on-headers rules editor (GET/PUT /admin/tagging/settings).
  // Declarative (config.yaml / TAGGING_HEADER_* env) rows come back with
  // managed=true and stay read-only; credential-header rejection happens
  // server-side and its message is surfaced from the PUT response.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { getJSON, sendJSON } from "$lib/api/client.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import {
    defaultTaggingHeader,
    normalizeTaggingHeaders,
    taggingSettingsPayload,
    taggingErrorMessage,
  } from "./tagging-logic.js";

  let taggingHeaders = $state([]);
  let editable = $state(true);
  let loading = $state(false);
  let saving = $state(false);
  let notice = $state("");
  let error = $state("");

  function addHeader() {
    taggingHeaders.push(defaultTaggingHeader());
  }

  function removeHeader(index) {
    const rule = taggingHeaders[index];
    if (!rule || rule.managed) {
      return;
    }
    taggingHeaders.splice(index, 1);
  }

  async function load() {
    loading = true;
    error = "";
    try {
      const result = await getJSON("/admin/tagging/settings", {
        label: "tagging settings",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        error = "Unable to load tagging settings.";
        return;
      }
      taggingHeaders = normalizeTaggingHeaders(result.data);
      editable = result.data && result.data.editable !== false;
    } catch (e) {
      console.error("Failed to fetch tagging settings:", e);
      error = "Unable to load tagging settings.";
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (saving || !editable) {
      return;
    }
    saving = true;
    notice = "";
    error = "";
    try {
      const result = await sendJSON(
        "/admin/tagging/settings",
        "PUT",
        taggingSettingsPayload(taggingHeaders),
        { label: "tagging settings" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        error =
          (result.status !== 401 && taggingErrorMessage(result.data)) ||
          "Unable to save tagging settings.";
        return;
      }
      taggingHeaders = normalizeTaggingHeaders(result.data);
      editable = result.data && result.data.editable !== false;
      notice = "Tagging settings saved.";
    } catch (e) {
      console.error("Failed to save tagging settings:", e);
      error = "Unable to save tagging settings.";
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    void auth.refreshTick;
    load();
  });
</script>

<div class="settings-refresh-section tagging-settings-section">
  <InlineHelpSection
    copyId="tagging-settings-help-copy"
    label="tagging help"
    text="Each request is labelled from the listed headers; labels land in usage tracking and audit logs. A header value can carry several labels split by the delimiter (default: comma). The prefix is trimmed from each label only — the header itself is forwarded unchanged unless 'Do not pass' is checked. Rows marked CONFIG come from config.yaml or TAGGING_HEADER_* env vars and are read-only here."
  >
    {#snippet title()}<h3>Tagging based on headers</h3>{/snippet}
  </InlineHelpSection>
  <div class="tagging-settings-grid" aria-describedby="tagging-settings-help-copy">
    {#each taggingHeaders as rule, index (index)}
      <div class="tagging-settings-row">
        <div class="form-field">
          <label class="form-field-label" for={"tagging-header-" + index}
            >Header</label
          >
          <input
            id={"tagging-header-" + index}
            class="form-input"
            type="text"
            placeholder="X-My-Tags"
            bind:value={rule.header}
            disabled={rule.managed || !editable}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for={"tagging-prefix-" + index}
            >Prefix to trim (optional)</label
          >
          <input
            id={"tagging-prefix-" + index}
            class="form-input"
            type="text"
            placeholder="tag-"
            bind:value={rule.prefix}
            disabled={rule.managed || !editable}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for={"tagging-delimiter-" + index}
            >Delimiter</label
          >
          <input
            id={"tagging-delimiter-" + index}
            class="form-input"
            type="text"
            placeholder=","
            bind:value={rule.delimiter}
            disabled={rule.managed || !editable}
          />
        </div>
        <label class="tagging-do-not-pass">
          <input
            type="checkbox"
            bind:checked={rule.do_not_pass}
            disabled={rule.managed || !editable}
          />
          <span>Do not pass upstream</span>
        </label>
        <div class="tagging-row-trailer">
          {#if rule.managed}
            <span
              class="badge"
              title="Declared in config.yaml or TAGGING_HEADER_* env vars; read-only here."
              >config</span
            >
          {:else}
            <button
              type="button"
              class="pagination-btn pagination-btn-danger-outline"
              disabled={!editable}
              aria-label={"Remove tagging header " + (rule.header || index + 1)}
              onclick={() => removeHeader(index)}>Remove</button
            >
          {/if}
        </div>
      </div>
    {/each}
    {#if loading}
      <Spinner size={16} label="Loading tagging settings" />
    {/if}
    {#if !loading && taggingHeaders.length === 0}
      <p class="tagging-settings-empty">
        No tagging headers configured. Requests are not labelled.
      </p>
    {/if}
  </div>
  <div class="settings-refresh-actions tagging-settings-actions">
    <button
      type="button"
      class="pagination-btn pagination-btn-with-icon"
      disabled={!editable || saving || loading}
      onclick={addHeader}
    >
      <Icon name="plus" class="form-action-icon" />
      <span>Add Header</span>
    </button>
    <button
      type="button"
      class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
      disabled={!editable || saving || loading}
      aria-busy={saving ? "true" : "false"}
      onclick={save}
    >
      <Icon name="save" class="form-action-icon" />
      <span>Save Tagging Settings</span>
    </button>
  </div>
</div>
<div>
  {#if notice}
    <div class="alert alert-success settings-refresh-alert" role="status" aria-live="polite">
      {notice}
    </div>
  {/if}
  {#if error}
    <div class="alert alert-warning settings-refresh-alert" role="alert" aria-live="assertive">
      {error}
    </div>
  {/if}
</div>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .tagging-settings-grid {
    display: grid;
    gap: 12px;
  }

  .tagging-settings-row {
    display: grid;
    grid-template-columns: minmax(170px, 1fr) minmax(150px, 1fr) minmax(80px, 110px) auto minmax(90px, auto);
    align-items: end;
    gap: 12px;
  }

  .tagging-do-not-pass {
    align-items: center;
    color: var(--text);
    display: flex;
    font-size: 13px;
    gap: 6px;
    min-height: 35px;
    white-space: nowrap;
  }

  .tagging-row-trailer {
    align-items: center;
    display: flex;
    gap: 8px;
    min-height: 35px;
  }

  /* Font size comes from the global .settings-refresh-section p rule (14px),
     which always outranked the 13px declared here — dropped to keep the
     shipped look now that scoping would flip the outcome. */
  .tagging-settings-empty {
    color: var(--text-muted);
    margin: 0;
  }

  .tagging-settings-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
  }

  @media (max-width: 768px) {
    .tagging-settings-row {
        grid-template-columns: 1fr;
        align-items: stretch;
      }

    .tagging-settings-actions {
        width: 100%;
      }

    .tagging-settings-actions :global(.pagination-btn) {
        width: 100%;
      }
  }
</style>
