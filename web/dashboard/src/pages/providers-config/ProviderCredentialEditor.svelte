<script>
  // Provider credential editor modal (create + edit). Name and Type are
  // immutable once a provider exists; API keys and service-account secrets
  // round-trip as "***********" masks that preserve the stored value.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { providersConfig } from "./providersConfig.svelte.js";
  import {
    providerCredentialTypeOptions,
    suggestProviderCredentialName,
  } from "./providersConfigLogic.js";

  const typeOptions = $derived(
    providerCredentialTypeOptions(providersConfig.types, providersConfig.form.type),
  );

  // onTypeChange resets the Name field to a fresh suggestion whenever the
  // Type selection changes while creating a provider (Type is immutable once
  // a provider exists, so this never runs in edit mode). The operator can
  // still edit the suggested name before saving.
  function onTypeChange() {
    if (providersConfig.formMode !== "create") {
      return;
    }
    providersConfig.form.name = suggestProviderCredentialName(
      providersConfig.rows,
      providersConfig.form.type,
    );
  }
</script>

<Modal open={providersConfig.formOpen} variant="editor" onclose={() => providersConfig.closeForm()}>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label="Provider credential editor">
    <form
      class="form"
      onsubmit={(event) => {
        event.preventDefault();
        providersConfig.submitForm();
      }}
    >
      <div class="editor-header">
        <div>
          <h3>{providersConfig.formMode === "edit" ? "Edit Provider" : "Add Provider"}</h3>
          <p class="form-hint">The name is the stable identity used for routing and cannot change once created.</p>
        </div>
        <DialogCloseButton
          label="Close provider editor"
          onclick={() => providersConfig.closeForm()}
        />
      </div>

      {#if providersConfig.error}
        <p class="form-error" role="alert" aria-live="assertive">{providersConfig.error}</p>
      {/if}

      <div class="form-field">
        <label class="form-field-label" for="provider-credential-type">Type</label>
        <select
          id="provider-credential-type"
          class="form-select"
          bind:value={providersConfig.form.type}
          disabled={providersConfig.formMode === "edit"}
          onchange={onTypeChange}
          data-modal-autofocus
        >
          <option value="" disabled>Select a provider type&hellip;</option>
          {#each typeOptions as type (type)}
            <option value={type}>{type}</option>
          {/each}
        </select>
        <small class="form-hint">Determines which fields the gateway uses to build requests.</small>
      </div>

      <div class="form-field">
        <label class="form-field-label" for="provider-credential-name">Name</label>
        <input
          id="provider-credential-name"
          type="text"
          class="mono"
          placeholder="my-openai"
          bind:value={providersConfig.form.name}
          disabled={providersConfig.formMode === "edit"}
        />
        {#if providersConfig.formMode === "create"}
          <small class="form-hint">Suggested from the selected type; used to route requests to this provider instance and editable before saving.</small>
        {:else}
          <small class="form-hint">Immutable once created.</small>
        {/if}
      </div>

      <div class="form-field">
        <label class="form-field-label" for="provider-credential-api-key-0">API Keys</label>
        <div class="vm-target-list">
          {#each providersConfig.form.api_keys as key, index (index)}
            <div class="vm-target-row">
              <input
                id={"provider-credential-api-key-" + index}
                type="text"
                class="mono vm-target-model"
                placeholder="sk-..."
                bind:value={key.value}
                aria-label="API key value"
              />
              <TableActionButton
                label="Remove API key"
                class="table-action-btn-danger table-icon-btn vm-target-remove"
                onclick={() => providersConfig.removeApiKeyRow(index)}
              >
                <Icon name="trash-2" class="table-icon-svg" />
              </TableActionButton>
            </div>
          {/each}
        </div>
        <div class="failover-target-actions">
          <button
            type="button"
            class="btn btn-with-icon"
            onclick={() => providersConfig.addApiKeyRow()}
          >
            <Icon name="plus" class="form-action-icon" />
            <span>Add key</span>
          </button>
        </div>
        <small class="form-hint">Multiple keys rotate round-robin. Saved values are shown as <code>***********</code>; leave the asterisks unchanged to keep the stored key. Not every provider type needs a key (e.g. Ollama, or Vertex/Bedrock using ADC).</small>
      </div>

      <div class="vm-status-row">
        <div class="vm-status-toggle">
          <button
            type="button"
            class="alias-toggle"
            class:enabled={providersConfig.form.enabled}
            aria-label={(providersConfig.form.enabled ? "Disable" : "Enable") + " provider"}
            onclick={() => (providersConfig.form.enabled = !providersConfig.form.enabled)}
          >
            <span class="alias-toggle-track"><span class="alias-toggle-thumb"></span></span>
            <span>{providersConfig.form.enabled ? "Enabled" : "Disabled"}</span>
          </button>
        </div>
      </div>

      <details
        class="mcp-server-advanced"
        open={providersConfig.advancedOpen}
        ontoggle={(event) => (providersConfig.advancedOpen = event.currentTarget.open)}
      >
        <summary>
          <span class="mcp-server-advanced-summary-copy">
            <span class="mcp-server-advanced-title">Advanced settings</span>
            <span class="form-hint">Base URL, models, API version, auth mode, and Vertex/Bedrock/service-account fields</span>
          </span>
        </summary>

        <div class="mcp-server-advanced-fields">
          <div class="form-field">
            <label class="form-field-label" for="provider-credential-base-url">Base URL</label>
            <input id="provider-credential-base-url" type="text" class="mono" placeholder="Optional endpoint override" bind:value={providersConfig.form.base_url} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-models">Models (optional, comma-separated)</label>
            <input id="provider-credential-models" type="text" class="mono" placeholder="gpt-4o, gpt-4o-mini" bind:value={providersConfig.form.models} />
            <small class="form-hint">Leave empty to auto-discover models from the provider's <code>/models</code> endpoint where supported.</small>
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-api-version">API Version</label>
            <input id="provider-credential-api-version" type="text" class="mono" placeholder="Optional, e.g. Azure API version" bind:value={providersConfig.form.api_version} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-backend">Backend</label>
            <input id="provider-credential-backend" type="text" class="mono" placeholder="Optional backend selector" bind:value={providersConfig.form.backend} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-auth-type">Auth Type</label>
            <input id="provider-credential-auth-type" type="text" class="mono" placeholder="Optional auth mode override" bind:value={providersConfig.form.auth_type} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-api-mode">API Mode</label>
            <input id="provider-credential-api-mode" type="text" class="mono" placeholder="Optional, e.g. auto, openai, standard" bind:value={providersConfig.form.api_mode} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-vertex-project">Vertex Project</label>
            <input id="provider-credential-vertex-project" type="text" class="mono" bind:value={providersConfig.form.vertex_project} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-vertex-location">Vertex Location</label>
            <input id="provider-credential-vertex-location" type="text" class="mono" bind:value={providersConfig.form.vertex_location} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-service-account-file">Service Account File</label>
            <input id="provider-credential-service-account-file" type="text" class="mono" placeholder="/path/to/service-account.json" bind:value={providersConfig.form.service_account_file} />
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-service-account-json">Service Account JSON</label>
            <textarea id="provider-credential-service-account-json" rows="4" class="mono" placeholder="Paste service account JSON" bind:value={providersConfig.form.service_account_json}></textarea>
            <small class="form-hint">Saved values are shown as <code>***********</code>; leave the asterisks unchanged to keep the stored value, or clear it to remove.</small>
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-service-account-json-base64">Service Account JSON (base64)</label>
            <input id="provider-credential-service-account-json-base64" type="text" class="mono" bind:value={providersConfig.form.service_account_json_base64} />
            <small class="form-hint">Saved values are shown as <code>***********</code>; leave the asterisks unchanged to keep the stored value.</small>
          </div>

          <div class="form-field">
            <label class="form-field-label" for="provider-credential-gcp-scope">GCP Scope</label>
            <input id="provider-credential-gcp-scope" type="text" class="mono" bind:value={providersConfig.form.gcp_scope} />
          </div>
        </div>
      </details>

      <div class="form-actions">
        <button type="button" class="btn" onclick={() => providersConfig.closeForm()}>Cancel</button>
        <button
          type="submit"
          class="btn btn-primary btn-with-icon"
          disabled={providersConfig.formSubmitting}
        >
          <Icon name="save" class="form-action-icon" />
          <span>{providersConfig.formSubmitting ? "Saving..." : "Save"}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>
