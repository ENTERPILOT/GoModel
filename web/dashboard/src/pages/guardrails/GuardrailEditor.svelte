<script>
  // Guardrail editor modal (EditorDialog shell). The form fields below
  // Name/Type/Description/User Path are driven entirely by the type
  // definition schema returned by GET /admin/guardrails/types (field.input:
  // text/number/select/textarea/checkboxes).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { guardrailsStore as store } from "./guardrails.svelte.js";

  const isEdit = $derived(store.formMode === "edit");
</script>

<EditorDialog
  open={store.formOpen}
  title={isEdit ? "Edit Guardrail" : "Create Guardrail"}
  ariaLabel="Guardrail editor"
  error={store.error}
  submitting={store.formSubmitting}
  submitLabel="Save Guardrail"
  dialogClass="settings-guardrails-editor guardrails-editor-wide"
  onclose={() => store.closeForm()}
  onsubmit={() => store.submitForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">
      Workflows reference these names directly, so renames are
      intentionally avoided after creation.
    </p>
  {/snippet}

  <div class="form-grid">
    <FormField id="guardrail-name" label="Name">
      <input
        id="guardrail-name"
        type="text"
        placeholder="safety-system-prompt"
        bind:value={store.form.name}
        disabled={isEdit}
        data-modal-autofocus={isEdit ? undefined : true}
      />
    </FormField>

    <FormField id="guardrail-type" label="Type">
      <select
        id="guardrail-type"
        class="form-select settings-select"
        value={store.form.type}
        disabled={isEdit}
        onchange={(event) => store.changeType(event.currentTarget.value)}
      >
        {#each store.types as typeDef (typeDef.type)}
          <option value={typeDef.type}>{typeDef.label}</option>
        {/each}
      </select>
    </FormField>

    <FormField id="guardrail-description" label="Description">
      <input
        id="guardrail-description"
        type="text"
        placeholder="What this guardrail is meant to do"
        bind:value={store.form.description}
        data-modal-autofocus={isEdit ? true : undefined}
      />
    </FormField>

    <div class="form-field">
      <InlineHelpSection
        copyId="guardrail-user-path-help-copy"
        label="guardrail user path help"
        text="Only used for auxiliary rewrite (llm_based_altering) guardrails; ignored for other guardrail types."
      >
        {#snippet title()}
          <label class="form-field-label" for="guardrail-user-path"
            >User Path</label
          >
        {/snippet}
      </InlineHelpSection>
      <input
        id="guardrail-user-path"
        type="text"
        placeholder="/team/alpha"
        bind:value={store.form.user_path}
        aria-label="Guardrail user path"
        aria-describedby="guardrail-user-path-help-copy"
      />
    </div>

    {#each store.typeFields(store.form.type) as field (field.key)}
      {#if field.input !== "checkboxes"}
        <div class="form-field form-field-wide">
          <InlineHelpSection
            copyId={"guardrail-field-help-" + field.key}
            label={field.label + " help"}
            text={field.help || ""}
          >
            {#snippet title()}
              <label
                class="form-field-label"
                for={"guardrail-field-" + field.key}>{field.label}</label
              >
            {/snippet}
          </InlineHelpSection>
          {#if field.input === "select"}
            <select
              class="form-select settings-select"
              id={"guardrail-field-" + field.key}
              value={store.fieldValue(field)}
              aria-describedby={field.help
                ? "guardrail-field-help-" + field.key
                : undefined}
              onchange={(event) =>
                store.setFieldValue(field, event.currentTarget.value)}
            >
              {#each field.options || [] as option (option.value)}
                <option value={option.value}>{option.label}</option>
              {/each}
            </select>
          {:else if field.input === "textarea"}
            <textarea
              id={"guardrail-field-" + field.key}
              placeholder={field.placeholder || ""}
              value={store.fieldValue(field)}
              aria-describedby={field.help
                ? "guardrail-field-help-" + field.key
                : undefined}
              oninput={(event) =>
                store.setFieldValue(field, event.currentTarget.value)}
            ></textarea>
          {:else}
            <input
              id={"guardrail-field-" + field.key}
              type={field.input || "text"}
              placeholder={field.placeholder || ""}
              value={store.fieldValue(field)}
              aria-describedby={field.help
                ? "guardrail-field-help-" + field.key
                : undefined}
              oninput={(event) =>
                store.setFieldValue(field, event.currentTarget.value)}
            />
          {/if}
        </div>
      {:else}
        <fieldset
          class="form-field form-field-wide form-field-fieldset"
          aria-describedby={field.help
            ? "guardrail-field-help-" + field.key
            : undefined}
        >
          <legend class="form-field-legend">{field.label}</legend>
          <div class="workflow-feature-toggles">
            {#each field.options || [] as option (field.key + "-" + option.value)}
              <label class="workflow-feature-toggle">
                <input
                  type="checkbox"
                  checked={store.arrayFieldSelected(field, option.value)}
                  onchange={(event) =>
                    store.toggleArrayFieldValue(
                      field,
                      option.value,
                      event.currentTarget.checked,
                    )}
                />
                <span>{option.label}</span>
              </label>
            {/each}
          </div>
          {#if field.help}
            <small class="form-hint" id={"guardrail-field-help-" + field.key}
              >{field.help}</small
            >
          {/if}
        </fieldset>
      {/if}
    {/each}
  </div>
</EditorDialog>

<style>
  /* EditorDialog renders the plain editor shell, so the wide width
     (min(1080px, 100%)) is applied on the dialog itself via the dialogClass
     hook. That div lives in EditorDialog's markup where this component's
     scope hash cannot match, so the rules are :global — compounded with
     .model-editor to keep outranking the shared base rule in forms.css. */
  :global(.model-editor.guardrails-editor-wide) {
    width: min(1080px, 100%);
  }
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

:global(.model-editor.settings-guardrails-editor) {
  min-width: 0;
}
</style>
