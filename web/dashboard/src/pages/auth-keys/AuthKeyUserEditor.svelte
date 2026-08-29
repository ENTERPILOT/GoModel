<script>
  // Assign-user modal for an existing API key (EditorDialog shell): bind the
  // key to a registered user so it follows the user's derived path, or unbind
  // it to freeze the key at its current path.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import { authKeyUserOptions } from "./authKeysLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const options = $derived(authKeyUserOptions(store.users));
</script>

<EditorDialog
  open={store.userEditor.open}
  title={m.api_keys_assign_user()}
  ariaLabel={m.api_keys_user_editor()}
  error={store.userEditor.error}
  submitting={store.userEditor.submitting}
  submitLabel={m.api_keys_save_user()}
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeUserEditor()}
  onsubmit={() => store.submitUserEditor()}
>
  {#snippet headerHint()}
    <p class="form-hint">{store.userEditor.name}</p>
  {/snippet}

  <FormField id="auth-key-user-edit" label={m.api_keys_user()}>
    <select id="auth-key-user-edit" data-modal-autofocus bind:value={store.userEditor.userId}>
      <option value="">{m.api_keys_user_none()}</option>
      {#each options as option (option.id)}
        <option value={option.id}>{option.label}</option>
      {/each}
    </select>
    <p class="form-hint">
      {m.api_keys_user_edit_help()}
    </p>
  </FormField>
</EditorDialog>
