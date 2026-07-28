<script>
  // Guardrail editor modal. The form fields below Name/Type/Description/User
  // Path are driven entirely by the type definition schema returned by
  // GET /admin/guardrails/types (field.input: text/number/select/textarea/
  // checkboxes).
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.ts";
  import { guardrailsStore as store } from "./guardrails.svelte.js";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";

  const isEdit = $derived(store.formMode === "edit");

  function onClose() {
    // Escape is ignored while the auth dialog is on top (stacked dialogs).
    if (auth.dialogOpen) return;
    store.closeForm();
  }
</script>

<Modal open={store.formOpen} variant="editor" onclose={onClose}>
  <div
    class="model-editor settings-guardrails-editor guardrails-editor-wide"
    role="dialog"
    aria-modal="true"
    aria-label="Guardrail editor"
  >
    <form
      class="form"
      onsubmit={(event) => {
        event.preventDefault();
        store.submitForm();
      }}
    >
      <div class="editor-header">
        <div>
          <h3>{isEdit ? "Edit Guardrail" : "Create Guardrail"}</h3>
          <p class="form-hint">
            Workflows reference these names directly, so renames are
            intentionally avoided after creation.
          </p>
        </div>
        <DialogCloseButton label="Close guardrail editor" onclick={() => store.closeForm()} />
      </div>

      {#if store.error}
        <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
      {/if}

      <div class="form-grid">
        <div class="form-field">
          <label class="form-field-label" for="guardrail-name">Name</label>
          <input
            id="guardrail-name"
            type="text"
            placeholder="safety-system-prompt"
            bind:value={store.form.name}
            disabled={isEdit}
            data-modal-autofocus={isEdit ? undefined : true}
          />
        </div>

        <div class="form-field">
          <label class="form-field-label" for="guardrail-type">Type</label>
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
        </div>

        <div class="form-field">
          <label class="form-field-label" for="guardrail-description"
            >Description</label
          >
          <input
            id="guardrail-description"
            type="text"
            placeholder="What this guardrail is meant to do"
            bind:value={store.form.description}
            data-modal-autofocus={isEdit ? true : undefined}
          />
        </div>

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

      <div class="form-actions">
        <button
          type="button"
          class="btn"
          onclick={() => store.closeForm()}>Cancel</button
        >
        <button
          type="submit"
          class="btn btn-primary btn-with-icon guardrail-submit-btn"
          disabled={store.formSubmitting}
        >
          <Icon name="save" class="form-action-icon" />
          <span>Save Guardrail</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

<style>
  /* The shared Modal atom renders the plain shell, so the wide width
     (min(1080px, 100%)) is applied on the dialog itself. */
  .guardrails-editor-wide {
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

.settings-guardrails-editor {
  min-width: 0;
}
</style>
