<script>
  // Provider credential editor modal (create + edit), built on the shared
  // EditorDialog shell. Name and Type are immutable once a provider exists;
  // API keys and service-account secrets round-trip as "***********" masks
  // that preserve the stored value.
  //
  // Everything below Name is rendered from the selected type's credential
  // schema, so an operator only ever sees the fields that type actually uses.
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import ProviderCredentialField from "./ProviderCredentialField.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { providersConfig } from "./providersConfig.svelte.js";
  import {
    providerCredentialTypeOptions,
    suggestProviderCredentialName,
  } from "./providersConfigLogic.js";
  import { Plus, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const typeOptions = $derived(
    providerCredentialTypeOptions(providersConfig.types, providersConfig.form.type),
  );
  const fields = $derived(providersConfig.formFields);
  const nameError = $derived(providersConfig.fieldErrors.name || "");
  const typeError = $derived(providersConfig.fieldErrors.type || "");
  const tripOnError = $derived(providersConfig.fieldErrors.trip_on || "");

  // The id lands on the first rule's match input (or the add button while
  // there are no rows), so a rejected save can scroll/focus the block the
  // same way it does for the schema-driven fields.
  const tripOnId = "provider-credential-trip_on";
  const tripOnTargetId = $derived(
    providersConfig.form.trip_on.length > 0 ? tripOnId + "-match-0" : tripOnId + "-add",
  );

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
    // "trip_on" wraps a rule list: focus the derived target element
    // (first rule's match input or the add button).
    let element;
    if (target === "trip_on") {
      element = document.getElementById(tripOnTargetId);
    } else {
      element = document.getElementById("provider-credential-" + target);
    }
    if (element) {
      element.scrollIntoView({ block: "center" });
      element.focus({ preventScroll: true });
    }
  });
</script>

<EditorDialog
  open={providersConfig.formOpen}
  title={providersConfig.formMode === "edit" ? m.providers_edit() : m.providers_add()}
  ariaLabel={m.providers_editor()}
  error={providersConfig.error}
  submitting={providersConfig.formSubmitting}
  novalidate
  onclose={() => providersConfig.closeForm()}
  onsubmit={() => providersConfig.submitForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">{m.providers_identity_help()}</p>
  {/snippet}

  <div class="form-field">
    <label class="form-field-label" for="provider-credential-type">
      {m.providers_type()}<span class="form-field-required" aria-hidden="true">*</span>
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
      <option value="" disabled>{m.providers_select_type()}</option>
      {#each typeOptions as type (type)}
        <option value={type}>{type}</option>
      {/each}
    </select>
    {#if typeError}
      <small class="form-field-error" id="provider-credential-type-error" role="alert">{typeError}</small>
    {:else}
      <small class="form-hint" id="provider-credential-type-hint">{m.providers_type_help()}</small>
    {/if}
  </div>

  <div class="form-field">
    <label class="form-field-label" for="provider-credential-name">
      {m.providers_name()}<span class="form-field-required" aria-hidden="true">*</span>
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
      <small class="form-hint" id="provider-credential-name-hint">{m.providers_name_create_help()}</small>
    {:else}
      <small class="form-hint" id="provider-credential-name-hint">{m.providers_name_edit_help()}</small>
    {/if}
  </div>

  {#if !providersConfig.form.type}
    <p class="form-hint">{m.providers_pick_type()}</p>
  {/if}

  {#each fields.primary as field (field.name)}
    <ProviderCredentialField {field} />
  {/each}

  <div class="form-field" id={tripOnId}>
    <label class="form-field-label" for={tripOnTargetId}>{m.providers_trip_on()}</label>
    <div class="vm-target-list">
      {#each providersConfig.form.trip_on as rule, index (index)}
        <div class="vm-target-row">
          <input
            id={index === 0 ? tripOnId + "-match-0" : undefined}
            type="text"
            class="mono vm-target-model"
            placeholder="insufficient_quota"
            aria-label={m.providers_trip_on_match({ number: index + 1 })}
            aria-invalid={tripOnError ? "true" : undefined}
            aria-describedby={index === 0 && tripOnError
              ? tripOnId + "-error"
              : undefined}
            bind:value={rule.match}
            oninput={() => providersConfig.clearFieldError("trip_on")}
          />
          <input
            type="text"
            class="mono provider-trip-rule-ttl"
            placeholder="e.g. 15m (optional)"
            aria-label={m.providers_trip_on_ttl({ number: index + 1 })}
            bind:value={rule.ttl}
            oninput={() => providersConfig.clearFieldError("trip_on")}
          />
          <TableActionButton
            label={m.providers_remove_trip_rule({ number: index + 1 })}
            class="table-action-btn-danger table-icon-btn vm-target-remove"
            onclick={() => providersConfig.removeTripRuleRow(index)}
          >
            <Icon icon={Trash2} class="table-icon-svg" />
          </TableActionButton>
        </div>
      {/each}
    </div>
    <div class="failover-target-actions">
      <button
        type="button"
        id={tripOnId + "-add"}
        class="btn btn-with-icon"
        onclick={() => providersConfig.addTripRuleRow()}
      >
        <Icon icon={Plus} class="form-action-icon" />
        <span>{m.providers_add_trip_rule()}</span>
      </button>
    </div>
    {#if tripOnError}
      <small class="form-field-error" id={tripOnId + "-error"} role="alert">{tripOnError}</small>
    {:else}
      <small class="form-hint">{m.providers_trip_on_hint()}</small>
    {/if}
  </div>

  <div class="vm-status-row">
    <div class="vm-status-toggle">
      <EnabledToggle
        enabled={providersConfig.form.enabled}
        label={m.providers_provider_toggle()}
        onclick={() => (providersConfig.form.enabled = !providersConfig.form.enabled)}
      />
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
          <span class="mcp-server-advanced-title">{m.providers_advanced()}</span>
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
</EditorDialog>

<style>
  /* Trip-rule TTL column stays narrow next to the free-form match pattern. */
  .provider-trip-rule-ttl {
    flex: 0 0 120px;
    width: 120px;
  }
</style>
