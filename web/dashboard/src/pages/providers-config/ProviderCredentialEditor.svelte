<script>
  // Provider credential editor modal (create + edit). Name and Type are
  // immutable once a provider exists; API keys and service-account secrets
  // round-trip as "***********" masks that preserve the stored value.
  //
  // Everything below Name is rendered from the selected type's credential
  // schema, so an operator only ever sees the fields that type actually uses.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import ProviderCredentialField from "./ProviderCredentialField.svelte";
  import { providersConfig } from "./providersConfig.svelte.js";
  import {
    providerCredentialTypeOptions,
    suggestProviderCredentialName,
  } from "./providersConfigLogic.js";

  const typeOptions = $derived(
    providerCredentialTypeOptions(providersConfig.types, providersConfig.form.type),
  );
  const fields = $derived(providersConfig.formFields);
  const nameError = $derived(providersConfig.fieldErrors.name || "");
  const typeError = $derived(providersConfig.fieldErrors.type || "");

  // onTypeChange resets the Name field to a fresh suggestion whenever the
  // Type selection changes while creating a provider (Type is immutable once
  // a provider exists, so this never runs in edit mode). The operator can
  // still edit the suggested name before saving.
  function onTypeChange() {
    providersConfig.selectType();
    if (providersConfig.formMode !== "create") {
      return;
    }
    providersConfig.form.name = suggestProviderCredentialName(
      providersConfig.rows,
      providersConfig.form.type,
    );
  }

  // A rejected save points at the field that caused it; move there so the
  // message is not left off-screen or inside a section the operator has to
  // find on their own.
  $effect(() => {
    const target = providersConfig.focusField;
    if (!target) {
      return;
    }
    providersConfig.focusField = "";
    const element = document.getElementById("provider-credential-" + target);
    if (element) {
      element.scrollIntoView({ block: "center" });
      element.focus({ preventScroll: true });
    }
  });
</script>

<Modal open={providersConfig.formOpen} variant="editor" onclose={() => providersConfig.closeForm()}>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label="Provider credential editor">
    <form
      class="form"
      novalidate
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
        <label class="form-field-label" for="provider-credential-type">
          Type<span class="form-field-required" aria-hidden="true">*</span>
        </label>
        <select
          id="provider-credential-type"
          class="form-select"
          bind:value={providersConfig.form.type}
          disabled={providersConfig.formMode === "edit"}
          aria-invalid={typeError ? "true" : undefined}
          aria-describedby={typeError ? "provider-credential-type-error" : "provider-credential-type-hint"}
          onchange={onTypeChange}
          data-modal-autofocus
        >
          <option value="" disabled>Select a provider type&hellip;</option>
          {#each typeOptions as type (type)}
            <option value={type}>{type}</option>
          {/each}
        </select>
        {#if typeError}
          <small class="form-field-error" id="provider-credential-type-error" role="alert">{typeError}</small>
        {:else}
          <small class="form-hint" id="provider-credential-type-hint">Determines which fields the gateway uses to build requests.</small>
        {/if}
      </div>

      <div class="form-field">
        <label class="form-field-label" for="provider-credential-name">
          Name<span class="form-field-required" aria-hidden="true">*</span>
        </label>
        <input
          id="provider-credential-name"
          type="text"
          class="mono"
          placeholder="my-openai"
          bind:value={providersConfig.form.name}
          disabled={providersConfig.formMode === "edit"}
          aria-invalid={nameError ? "true" : undefined}
          aria-describedby={nameError ? "provider-credential-name-error" : "provider-credential-name-hint"}
          oninput={() => providersConfig.clearFieldError("name")}
        />
        {#if nameError}
          <small class="form-field-error" id="provider-credential-name-error" role="alert">{nameError}</small>
        {:else if providersConfig.formMode === "create"}
          <small class="form-hint" id="provider-credential-name-hint">Suggested from the selected type; used to route requests to this provider instance and editable before saving.</small>
        {:else}
          <small class="form-hint" id="provider-credential-name-hint">Immutable once created.</small>
        {/if}
      </div>

      {#if !providersConfig.form.type}
        <p class="form-hint">Pick a type to configure its credentials — each provider type asks for different settings.</p>
      {/if}

      {#each fields.primary as field (field.name)}
        <ProviderCredentialField {field} />
      {/each}

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

      {#if fields.advanced.length > 0}
        <details
          class="mcp-server-advanced"
          open={providersConfig.advancedOpen}
          ontoggle={(event) => (providersConfig.advancedOpen = event.currentTarget.open)}
        >
          <summary>
            <span class="mcp-server-advanced-summary-copy">
              <span class="mcp-server-advanced-title">Advanced settings</span>
              <span class="form-hint">{fields.advanced.map((field) => field.label).join(", ")}</span>
            </span>
          </summary>

          <div class="mcp-server-advanced-fields">
            {#each fields.advanced as field (field.name)}
              <ProviderCredentialField {field} />
            {/each}
          </div>
        </details>
      {/if}

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
