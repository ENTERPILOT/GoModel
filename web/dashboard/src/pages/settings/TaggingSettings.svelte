<script>
  // Tagging-based-on-headers rules editor (GET/PUT /admin/tagging/settings).
  // Declarative (config.yaml / TAGGING_HEADER_* env) rows come back with
  // managed=true and stay read-only; credential-header rejection happens
  // server-side and its message is surfaced from the PUT response.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { flash } from "$lib/stores/flash.svelte.js";
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
  import { Plus, Save } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let taggingHeaders = $state([]);
  let editable = $state(true);
  let loading = $state(false);
  let saving = $state(false);
  // Load failures only; save feedback goes through the flash store.
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
        error = m.settings_tagging_load_failed();
        return;
      }
      taggingHeaders = normalizeTaggingHeaders(result.data);
      editable = result.data && result.data.editable !== false;
    } catch (e) {
      console.error("Failed to fetch tagging settings:", e);
      error = m.settings_tagging_load_failed();
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (saving || !editable) {
      return;
    }
    saving = true;
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
        flash.error(
          (result.status !== 401 && taggingErrorMessage(result.data)) ||
            m.settings_tagging_save_failed(),
        );
        return;
      }
      taggingHeaders = normalizeTaggingHeaders(result.data);
      editable = result.data && result.data.editable !== false;
      // A successful save proves the endpoint works and delivered fresh
      // data, so a load error from a failed earlier fetch is obsolete.
      error = "";
      flash.success(m.settings_tagging_saved());
    } catch (e) {
      console.error("Failed to save tagging settings:", e);
      flash.error(m.settings_tagging_save_failed());
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
    label={m.settings_tagging_help_label()}
    text={m.settings_tagging_help()}
  >
    {#snippet title()}<h3>{m.settings_tagging_title()}</h3>{/snippet}
  </InlineHelpSection>
  <div class="tagging-settings-grid" aria-describedby="tagging-settings-help-copy">
    {#each taggingHeaders as rule, index (index)}
      <div class="tagging-settings-row">
        <div class="form-field">
          <label class="form-field-label" for={"tagging-header-" + index}
            >{m.settings_tagging_header()}</label
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
            >{m.settings_tagging_prefix()}</label
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
            >{m.settings_tagging_delimiter()}</label
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
          <span>{m.settings_tagging_do_not_pass()}</span>
        </label>
        <div class="tagging-row-trailer">
          {#if rule.managed}
            <span
              class="badge"
              title={m.settings_tagging_config_help()}
              >config</span
            >
          {:else}
            <button
              type="button"
              class="btn btn-danger-outline"
              disabled={!editable}
              aria-label={m.settings_tagging_remove_label({
                header: rule.header || index + 1,
              })}
              onclick={() => removeHeader(index)}>{m.settings_tagging_remove()}</button
            >
          {/if}
        </div>
      </div>
    {/each}
    {#if loading}
      <Spinner size={16} label={m.settings_tagging_loading()} />
    {/if}
    {#if !loading && taggingHeaders.length === 0}
      <p class="tagging-settings-empty">
        {m.settings_tagging_empty()}
      </p>
    {/if}
  </div>
  <div class="settings-refresh-actions tagging-settings-actions">
    <button
      type="button"
      class="btn btn-with-icon"
      disabled={!editable || saving || loading}
      onclick={addHeader}
    >
      <Icon icon={Plus} class="form-action-icon" />
      <span>{m.settings_tagging_add()}</span>
    </button>
    <button
      type="button"
      class="btn btn-primary btn-with-icon"
      disabled={!editable || saving || loading}
      aria-busy={saving ? "true" : "false"}
      onclick={save}
    >
      <Icon icon={Save} class="form-action-icon" />
      <span>{m.settings_tagging_save()}</span>
    </button>
  </div>
</div>
<div>
  {#if error}
    <div class="alert alert-warning settings-refresh-alert" role="alert" aria-live="assertive">
      {error}
    </div>
  {/if}
</div>

<style>
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

    .tagging-settings-actions :global(.btn) {
        width: 100%;
      }
  }
</style>
