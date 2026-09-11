<script>
  // SchemaFields — renders a list of schema-driven form fields (see
  // $lib/utils/schemaFields.js for the field shape) bound to a plain config
  // object. Shared by the guardrail editor (instance config) and the
  // virtual-model editor (plugin route-strategy config).
  //
  // Props: fields, config, idPrefix (element ids are `${idPrefix}-${key}`),
  // disabled, onchange(nextConfig). The component never mutates `config`;
  // every edit produces a new object through the pure helpers.
  import InlineHelpSection from "./InlineHelpSection.svelte";
  import SearchSelect from "./SearchSelect.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { modelPickerOptions } from "$lib/utils/modelSelectors.js";
  import {
    isSecretPlaceholder,
    schemaArrayFieldSelected,
    schemaFieldValue,
    setSchemaFieldValue,
    toggleSchemaArrayValue,
  } from "$lib/utils/schemaFields.js";
  import * as m from "$lib/paraglide/messages.js";

  let {
    fields = [],
    config = {},
    idPrefix = "schema-field",
    disabled = false,
    onchange,
  } = $props();

  // `model` fields pick from the shared inventory (loaded at startup) the
  // same way the playground and virtual-model editors do; aliases and
  // virtual models are typed in as custom values.
  const modelOptions = $derived(modelPickerOptions(modelsStore.models));

  function emit(next) {
    onchange?.(next);
  }

  function set(field, value) {
    emit(setSchemaFieldValue(config, field, value));
  }

  function toggle(field, optionValue, checked) {
    emit(toggleSchemaArrayValue(config, field, optionValue, checked));
  }

  function inputType(field) {
    switch (field.input) {
      case "number":
        return "number";
      case "secret":
        return "password";
      default:
        return "text";
    }
  }

  function placeholder(field) {
    if (field.placeholder) return field.placeholder;
    if (field.input === "model") return m.schema_fields_model_placeholder();
    if (field.input === "list") return m.schema_fields_list_placeholder();
    return "";
  }

  // A bool renders as one toggle row like a checkbox group, so it shares
  // that branch's layout instead of the labelled input layout.
  function isToggleRow(field) {
    return field.input === "checkboxes" || field.input === "bool";
  }

  function helpId(field) {
    return field.help ? idPrefix + "-help-" + field.key : undefined;
  }
</script>

{#each fields as field (field.key)}
  {#if !isToggleRow(field)}
    <div class="form-field form-field-wide">
      <InlineHelpSection
        copyId={idPrefix + "-help-" + field.key}
        label={field.label + " help"}
        text={field.help || ""}
      >
        {#snippet title()}
          <label class="form-field-label" for={idPrefix + "-" + field.key}
            >{field.label}{#if field.required}<span
                class="schema-field-required"
                aria-hidden="true">*</span
              >{/if}</label
          >
        {/snippet}
      </InlineHelpSection>
      {#if field.input === "select"}
        <select
          class="form-select settings-select"
          id={idPrefix + "-" + field.key}
          value={schemaFieldValue(config, field)}
          aria-describedby={helpId(field)}
          {disabled}
          onchange={(event) => set(field, event.currentTarget.value)}
        >
          {#each field.options || [] as option (option.value)}
            <option value={option.value}>{option.label}</option>
          {/each}
        </select>
      {:else if field.input === "textarea"}
        <textarea
          id={idPrefix + "-" + field.key}
          placeholder={placeholder(field)}
          value={schemaFieldValue(config, field)}
          aria-describedby={helpId(field)}
          {disabled}
          oninput={(event) => set(field, event.currentTarget.value)}
        ></textarea>
      {:else if field.input === "list"}
        <textarea
          id={idPrefix + "-" + field.key}
          class="mono"
          placeholder={placeholder(field)}
          value={schemaFieldValue(config, field).join("\n")}
          aria-describedby={helpId(field)}
          {disabled}
          onchange={(event) => set(field, event.currentTarget.value)}
        ></textarea>
      {:else if field.input === "model"}
        <SearchSelect
          id={idPrefix + "-" + field.key}
          options={modelOptions}
          value={schemaFieldValue(config, field)}
          onchange={(value) => set(field, value)}
          placeholder={placeholder(field)}
          searchPlaceholder={m.schema_fields_model_search_placeholder()}
          ariaLabel={field.label}
          {disabled}
          allowCustom
          mono
        />
      {:else}
        <input
          id={idPrefix + "-" + field.key}
          type={inputType(field)}
          placeholder={placeholder(field)}
          value={schemaFieldValue(config, field)}
          autocomplete={field.input === "secret" ? "new-password" : undefined}
          aria-describedby={helpId(field)}
          {disabled}
          oninput={(event) => set(field, event.currentTarget.value)}
        />
        {#if field.input === "secret" && isSecretPlaceholder(schemaFieldValue(config, field))}
          <small class="form-hint">{m.schema_fields_secret_stored()}</small>
        {/if}
      {/if}
    </div>
  {:else if field.input === "bool"}
    <div class="form-field form-field-wide">
      <label class="workflow-feature-toggle">
        <input
          id={idPrefix + "-" + field.key}
          type="checkbox"
          checked={schemaFieldValue(config, field)}
          aria-describedby={helpId(field)}
          {disabled}
          onchange={(event) => set(field, event.currentTarget.checked)}
        />
        <span>{field.label}</span>
      </label>
      {#if field.help}
        <small class="form-hint" id={idPrefix + "-help-" + field.key}
          >{field.help}</small
        >
      {/if}
    </div>
  {:else}
    <fieldset
      class="form-field form-field-wide form-field-fieldset"
      aria-describedby={helpId(field)}
      {disabled}
    >
      <legend class="form-field-legend">{field.label}</legend>
      <div class="workflow-feature-toggles">
        {#each field.options || [] as option (field.key + "-" + option.value)}
          <label class="workflow-feature-toggle">
            <input
              type="checkbox"
              checked={schemaArrayFieldSelected(config, field, option.value)}
              onchange={(event) =>
                toggle(field, option.value, event.currentTarget.checked)}
            />
            <span>{option.label}</span>
          </label>
        {/each}
      </div>
      {#if field.help}
        <small class="form-hint" id={idPrefix + "-help-" + field.key}
          >{field.help}</small
        >
      {/if}
    </fieldset>
  {/if}
{/each}

<style>
  .form-field-fieldset {
    border: 0;
    margin: 0;
    min-inline-size: 0;
    padding: 0;
  }

  .form-field-legend {
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.5px;
    padding: 0;
    text-transform: uppercase;
  }

  .schema-field-required {
    margin-left: 3px;
    color: var(--danger);
  }
</style>
