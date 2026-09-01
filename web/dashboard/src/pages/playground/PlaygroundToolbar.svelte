<script>
  // Top controls: endpoint, model, streaming, and the "add a message" buttons
  // that grow the editable conversation history.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { Eraser, Plus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as store } from "./playground.svelte.js";
  import { playgroundUserPathOptions, ROLES } from "./playgroundLogic.js";
  import { modelsStore } from "$lib/stores/models.svelte.js";

  let { modelOptions = [] } = $props();

  const selectOptions = $derived(
    modelOptions.map((option) => ({
      value: option.id,
      label: option.label,
      description: option.provider,
    })),
  );

  const userPathOptions = $derived(playgroundUserPathOptions(modelsStore.models, store.model));

  const endpointOptions = $derived([
    { value: "chat", label: m.playground_endpoint_chat() },
    { value: "responses", label: m.playground_endpoint_responses() },
    { value: "messages", label: m.playground_endpoint_messages() },
  ]);

  const roleLabels = {
    system: m.playground_role_system,
    user: m.playground_role_user,
    assistant: m.playground_role_assistant,
  };
</script>

<div class="playground-toolbar">
  <div class="playground-toolbar-row">
    <label class="playground-field">
      <span class="playground-field-label">{m.playground_endpoint_label()}</span>
      <SegmentedControl
        options={endpointOptions}
        value={store.endpoint}
        ariaLabel={m.playground_endpoint_label()}
        onchange={(value) => store.setEndpoint(value)}
      />
    </label>
    <div class="playground-field playground-field-model">
      <label class="playground-field-label" for="playground-model">{m.playground_model_label()}</label>
      <SearchSelect
        id="playground-model"
        class="playground-model-select"
        options={selectOptions}
        value={store.model}
        onchange={(value) => store.setModel(value)}
        placeholder={m.playground_model_placeholder()}
        searchPlaceholder={m.playground_model_search_placeholder()}
        ariaLabel={m.playground_model_label()}
        allowCustom
        mono
      />
    </div>
    <div class="playground-field playground-field-user-path" title={m.playground_user_path_help()}>
      <label class="playground-field-label" for="playground-user-path">{m.playground_user_path_label()}</label>
      <SearchSelect
        id="playground-user-path"
        class="playground-user-path-select"
        options={userPathOptions}
        value={store.userPath}
        onchange={(value) => store.setUserPath(value)}
        placeholder={m.playground_user_path_placeholder()}
        ariaLabel={m.playground_user_path_label()}
        allowCustom
        mono
      />
    </div>
  </div>

  <div class="playground-toolbar-row playground-toolbar-messages">
    <span class="playground-field-label">{m.playground_add_message()}</span>
    <div class="playground-add-buttons" role="group" aria-label={m.playground_add_message()}>
      {#each ROLES as role (role)}
        <button
          type="button"
          class={["btn", "btn-with-icon", "playground-add-btn", "playground-role-" + role]}
          onclick={() => store.addMessage(role)}
        >
          <Icon icon={Plus} class="table-icon-svg" />
          <span>{roleLabels[role]()}</span>
        </button>
      {/each}
    </div>
    <span class="playground-endpoint-path mono" title={m.playground_help()}>
      POST {store.endpointPath}
    </span>
    <label class="playground-stream-toggle">
      <input
        type="checkbox"
        checked={store.stream}
        onchange={(event) => store.setStream(event.currentTarget.checked)}
      />
      <span>{m.playground_stream_label()}</span>
    </label>
    <button
      type="button"
      class="btn btn-with-icon"
      disabled={store.messages.length === 0 && !store.response && !store.error}
      onclick={() => store.clear()}
    >
      <Icon icon={Eraser} class="table-icon-svg" />
      <span>{m.playground_clear()}</span>
    </button>
  </div>
</div>

<style>
  .playground-toolbar {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px 14px;
    margin-bottom: 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .playground-toolbar-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px 16px;
  }

  .playground-field {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .playground-field-model {
    flex: 1 1 240px;
    min-width: 200px;
  }

  /* The class lands on the molecule's wrapper, outside this scope hash. */
  .playground-field-model :global(.playground-model-select) {
    flex: 1 1 auto;
    min-width: 0;
  }

  .playground-field-user-path {
    flex: 0 1 260px;
  }

  .playground-field-user-path :global(.playground-user-path-select) {
    flex: 1 1 auto;
    min-width: 0;
  }

  .playground-field-label {
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.3px;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .playground-stream-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
    font-size: 13px;
    cursor: pointer;
    user-select: none;
  }

  .playground-stream-toggle input {
    accent-color: var(--accent);
  }

  .playground-add-buttons {
    display: inline-flex;
    gap: 6px;
  }

  .playground-add-btn {
    padding: 5px 12px;
    font-size: 12px;
    font-weight: 600;
  }

  .playground-add-btn.playground-role-system {
    color: var(--warning);
  }

  .playground-add-btn.playground-role-user {
    color: var(--info);
  }

  .playground-add-btn.playground-role-assistant {
    color: var(--success);
  }

  .playground-endpoint-path {
    flex: 1 1 0;
    min-width: 0;
    color: var(--text-muted);
    font-size: 12px;
    text-align: right;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  @media (max-width: 768px) {
    .playground-endpoint-path {
      display: none;
    }
  }
</style>
