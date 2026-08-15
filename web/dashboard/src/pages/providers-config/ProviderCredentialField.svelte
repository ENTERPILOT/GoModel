<script>
  // One credential field of the provider editor, rendered from the schema the
  // gateway serves for the selected provider type: label and hint from the
  // field's presentation, control from its shape (key list, enumeration,
  // multi-line secret, plain text), and an inline message when it is at fault.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { providersConfig } from "./providersConfig.svelte.js";
  import { Plus, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let { field } = $props();

  const id = $derived("provider-credential-" + field.name);
  const error = $derived(providersConfig.fieldErrors[field.name] || "");
  const describedBy = $derived(error ? id + "-error" : field.hint ? id + "-hint" : undefined);

  // The schema lists a field's canonical values, not every value its provider
  // accepts (`openai_compatible` and `compat` mean the same thing to Gemini).
  // A stored value outside the list joins it rather than being dropped: a
  // select with no matching option renders blank, and saving that would
  // silently clear a working setting.
  const options = $derived.by(() => {
    const stored = String(providersConfig.form[field.name] || "").trim();
    if (!stored || field.options.includes(stored)) {
      return field.options;
    }
    return [...field.options, stored];
  });

  function onInput() {
    providersConfig.clearFieldError(field.name);
  }
</script>

<div class="form-field">
  <label class="form-field-label" for={id}>
    {field.label}
    {#if field.required}<span class="form-field-required" aria-hidden="true">*</span>{/if}
  </label>

  {#if field.control === "keys"}
    <div class="vm-target-list">
      {#each providersConfig.form.api_keys as key, index (index)}
        <div class="vm-target-row">
          <input
            id={index === 0 ? id : id + "-" + index}
            type="text"
            class="mono vm-target-model"
            placeholder="sk-..."
            aria-label={m.providers_api_key({ number: index + 1 })}
            aria-invalid={error ? "true" : undefined}
            aria-describedby={index === 0 ? describedBy : undefined}
            bind:value={key.value}
            oninput={onInput}
          />
          <TableActionButton
            label={m.providers_remove_api_key({ number: index + 1 })}
            class="table-action-btn-danger table-icon-btn vm-target-remove"
            onclick={() => providersConfig.removeApiKeyRow(index)}
          >
            <Icon icon={Trash2} class="table-icon-svg" />
          </TableActionButton>
        </div>
      {/each}
    </div>
    <div class="failover-target-actions">
      <button
        type="button"
        id={providersConfig.form.api_keys.length === 0 ? id : undefined}
        class="btn btn-with-icon"
        onclick={() => providersConfig.addApiKeyRow()}
      >
        <Icon icon={Plus} class="form-action-icon" />
        <span>{m.providers_add_key()}</span>
      </button>
    </div>
  {:else if field.control === "select"}
    <select
      {id}
      class="form-select"
      aria-invalid={error ? "true" : undefined}
      aria-describedby={describedBy}
      bind:value={providersConfig.form[field.name]}
      onchange={onInput}
    >
      <option value="">{m.providers_default()}</option>
      {#each options as option (option)}
        <option value={option}>{option}</option>
      {/each}
    </select>
  {:else if field.control === "checkbox"}
    <input
      {id}
      type="checkbox"
      aria-invalid={error ? "true" : undefined}
      aria-describedby={describedBy}
      bind:checked={providersConfig.form[field.name]}
      onchange={onInput}
    />
  {:else if field.control === "textarea"}
    <textarea
      {id}
      rows="4"
      class="mono"
      placeholder={field.placeholder || ""}
      aria-invalid={error ? "true" : undefined}
      aria-describedby={describedBy}
      bind:value={providersConfig.form[field.name]}
      oninput={onInput}
    ></textarea>
  {:else}
    <input
      {id}
      type="text"
      class="mono"
      placeholder={field.placeholder || ""}
      aria-invalid={error ? "true" : undefined}
      aria-describedby={describedBy}
      bind:value={providersConfig.form[field.name]}
      oninput={onInput}
    />
  {/if}

  {#if error}
    <small class="form-field-error" id={id + "-error"} role="alert">{error}</small>
  {:else if field.hint}
    <small class="form-hint" id={id + "-hint"}>{field.hint}</small>
  {/if}
</div>
