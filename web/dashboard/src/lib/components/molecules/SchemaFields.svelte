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
    return "";
  }

  function helpId(field) {
    return field.help ? idPrefix + "-help-" + field.key : undefined;
  }
</script>

{#each fields as field (field.key)}
  {#if field.input !== "checkboxes"}
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
      {:else}
        <input
          id={idPrefix + "-" + field.key}
          type={inputType(field)}
          class:mono={field.input === "model"}
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
