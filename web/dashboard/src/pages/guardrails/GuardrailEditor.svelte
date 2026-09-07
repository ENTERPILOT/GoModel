<script>
  // Guardrail editor modal (EditorDialog shell). The form fields below
  // Name/Type/Description/User Path are driven entirely by the type
  // definition schema returned by GET /admin/guardrails/types (rendered by
  // the shared SchemaFields component: text/number/select/textarea/
  // checkboxes/secret/model). The Advanced area holds the per-instance
  // failure mode and timeout.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import SchemaFields from "$lib/components/molecules/SchemaFields.svelte";
  import { guardrailsStore as store } from "./guardrails.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const isEdit = $derived(store.formMode === "edit");
</script>

<EditorDialog
  open={store.formOpen}
  title={isEdit ? m.guardrails_edit() : m.guardrails_create()}
  ariaLabel={m.guardrails_editor()}
  error={store.error}
  submitting={store.formSubmitting}
  submitLabel={m.guardrails_save()}
  dialogClass="settings-guardrails-editor guardrails-editor-wide"
  onclose={() => store.closeForm()}
  onsubmit={() => store.submitForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">
      {m.guardrails_editor_help()}
    </p>
  {/snippet}

  <div class="form-grid">
    <FormField id="guardrail-name" label={m.guardrails_name()}>
      <input
        id="guardrail-name"
        type="text"
        placeholder="safety-system-prompt"
        bind:value={store.form.name}
        disabled={isEdit}
        data-modal-autofocus={isEdit ? undefined : true}
      />
    </FormField>

    <FormField id="guardrail-type" label={m.guardrails_type()}>
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

    <FormField id="guardrail-description" label={m.guardrails_description()}>
      <input
        id="guardrail-description"
        type="text"
        placeholder={m.guardrails_description_placeholder()}
        bind:value={store.form.description}
        data-modal-autofocus={isEdit ? true : undefined}
      />
    </FormField>

    <div class="form-field">
      <InlineHelpSection
        copyId="guardrail-user-path-help-copy"
        label={m.guardrails_user_path_help_label()}
        text={m.guardrails_user_path_help()}
      >
        {#snippet title()}
          <label class="form-field-label" for="guardrail-user-path"
            >{m.guardrails_user_path()}</label
          >
        {/snippet}
      </InlineHelpSection>
      <input
        id="guardrail-user-path"
        type="text"
        placeholder="/team/alpha"
        bind:value={store.form.user_path}
        aria-label={m.guardrails_user_path()}
        aria-describedby="guardrail-user-path-help-copy"
      />
    </div>

    <SchemaFields
      fields={store.typeFields(store.form.type)}
      config={store.form.config}
      idPrefix="guardrail-field"
      onchange={(config) => store.setConfig(config)}
    />

    <details class="form-field form-field-wide guardrail-advanced">
      <summary class="guardrail-advanced-summary">{m.guardrails_advanced()}</summary>
      <p class="form-hint">{m.guardrails_advanced_help()}</p>
      <div class="form-grid guardrail-advanced-grid">
        <FormField id="guardrail-fail-mode" label={m.guardrails_fail_mode()}>
          <select
            id="guardrail-fail-mode"
            class="form-select settings-select"
            bind:value={store.form.fail_mode}
          >
            <option value="">{m.guardrails_fail_mode_default()}</option>
            <option value="closed">{m.guardrails_fail_mode_closed()}</option>
            <option value="open">{m.guardrails_fail_mode_open()}</option>
          </select>
        </FormField>
        <FormField id="guardrail-timeout-ms" label={m.guardrails_timeout_ms()}>
          <input
            id="guardrail-timeout-ms"
            type="number"
            min="0"
            step="1"
            placeholder={m.guardrails_timeout_placeholder()}
            bind:value={store.form.timeout_ms}
          />
          <small class="form-hint">{m.guardrails_timeout_help()}</small>
        </FormField>
      </div>
    </details>
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

  :global(.model-editor.settings-guardrails-editor) {
    min-width: 0;
  }

  .guardrail-advanced {
    grid-column: 1 / -1;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg);
  }

  .guardrail-advanced-summary {
    cursor: pointer;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.5px;
    text-transform: uppercase;
    user-select: none;
  }

  .guardrail-advanced[open] .guardrail-advanced-summary {
    margin-bottom: 8px;
  }

  .guardrail-advanced-grid {
    margin-top: 10px;
  }
</style>
