<script>
  // Metadata override editor (EditorDialog shell): category, input media and
  // token limits for one provider model. Empty fields inherit.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { formatNumber } from "$lib/utils/format.js";
  import { metadataOverrides } from "./metadataOverrides.svelte.js";
  import {
    CAPABILITY_INHERIT,
    CAPABILITY_SUPPORTED,
    CAPABILITY_UNSUPPORTED,
    INPUT_CAPABILITIES,
    OVERRIDE_CATEGORIES,
  } from "./metadataOverridesLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const mo = metadataOverrides;

  const categoryLabels = {
    text_generation: m.models_category_text_generation,
    embedding: m.models_category_embedding,
    image: m.models_category_image,
    audio: m.models_category_audio,
    video: m.models_category_video,
    utility: m.models_category_utility,
  };

  // Placeholders show the value in effect now, so an empty field says what
  // it inherits.
  function currentTokens(field) {
    const value = mo.current && mo.current[field];
    return value == null ? m.models_metadata_inherit() : formatNumber(Number(value));
  }
</script>

<EditorDialog
  open={mo.formOpen}
  ariaLabel={m.models_metadata_editor()}
  error={mo.error}
  submitting={mo.submitting}
  submitLabel={m.models_metadata_save()}
  onclose={() => mo.close()}
  onsubmit={() => mo.submit()}
>
  {#snippet header()}
    <p class="form-kicker">{m.models_metadata_override()}</p>
    <h3>{mo.displayName}</h3>
  {/snippet}

  <p class="form-hint">{m.models_metadata_help()}</p>

  <fieldset class="metadata-fieldset">
    <legend class="form-field-label">{m.models_details_categories()}</legend>
    <div class="metadata-categories">
      {#each OVERRIDE_CATEGORIES as category (category)}
        <label class="metadata-category">
          <input
            type="checkbox"
            checked={mo.form.categories.includes(category)}
            onchange={(event) => mo.toggleCategory(category, event.currentTarget.checked)}
          />
          <span>{categoryLabels[category]()}</span>
        </label>
      {/each}
    </div>
    <small class="form-hint">{m.models_metadata_categories_help()}</small>
  </fieldset>

  <div class="form-grid">
    {#each INPUT_CAPABILITIES as capability (capability.key)}
      <FormField id={"metadata-capability-" + capability.key} label={m.models_metadata_accepts({ input: capability.label() })}>
        <select
          id={"metadata-capability-" + capability.key}
          class="form-select"
          bind:value={mo.form.capabilities[capability.key]}
        >
          <option value={CAPABILITY_INHERIT}>{m.models_metadata_inherit()}</option>
          <option value={CAPABILITY_SUPPORTED}>{m.models_details_supported()}</option>
          <option value={CAPABILITY_UNSUPPORTED}>{m.models_details_unsupported()}</option>
        </select>
      </FormField>
    {/each}

    <FormField id="metadata-context-window" label={m.models_details_context_window()}>
      <input
        id="metadata-context-window"
        type="number"
        min="1"
        step="1"
        inputmode="numeric"
        placeholder={currentTokens("context_window")}
        bind:value={mo.form.context_window}
      />
    </FormField>
    <FormField id="metadata-max-output-tokens" label={m.models_details_max_output_tokens()}>
      <input
        id="metadata-max-output-tokens"
        type="number"
        min="1"
        step="1"
        inputmode="numeric"
        placeholder={currentTokens("max_output_tokens")}
        bind:value={mo.form.max_output_tokens}
      />
    </FormField>
  </div>

  {#snippet extraActions()}
    {#if mo.hasExisting}
      <button
        type="button"
        class="btn btn-danger-outline"
        disabled={mo.submitting}
        onclick={() => mo.remove()}
      >
        {m.models_remove_override()}
      </button>
    {/if}
  {/snippet}
</EditorDialog>

<style>
  .metadata-fieldset {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-inline-size: 0;
    margin: 0;
    padding: 0;
    border: 0;
  }

  .metadata-categories {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 16px;
  }

  .metadata-category {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }
</style>
