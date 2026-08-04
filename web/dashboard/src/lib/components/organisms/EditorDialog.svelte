<script>
  // EditorDialog — the shared editor-modal shell used by every create/edit
  // dialog: Modal wrapper, editor header (title + close button), the form
  // element, the error banner, and the footer actions row.
  //
  // Props:
  //   open        — bind to the store's formOpen flag
  //   title       — heading text ("Edit Budget"); also the default aria-label
  //   ariaLabel   — accessible dialog name when it should differ from title
  //   error       — form-level error text (rendered above the actions row)
  //   submitting  — request in flight: disables the submit button and swaps
  //                 its label to submittingLabel
  //   submitDisabled — disables submit without the label swap (read-only
  //                 config-managed rows, a pending delete, ...)
  //   submitLabel / submittingLabel / submitIcon — submit button content
  //   cancel      — set false to drop the Cancel button (default true)
  //   dialogClass — extra classes on the dialog shell next to .model-editor
  //   novalidate  — skip native form validation (hidden-control traps)
  //   canClose    — extra guard consulted on Escape/backdrop close, for
  //                 editors that stack another dialog on top
  //   onclose     — close the editor (called for Cancel/close/Escape)
  //   onsubmit    — submit the form (preventDefault is already handled)
  // Snippets:
  //   header      — replaces the default <h3>{title}</h3> block
  //   headerHint  — optional extra content under the default title
  //   children    — the form fields
  //   extraActions — extra footer buttons between Cancel and submit
  //
  // Escape/backdrop closes are ignored while the auth dialog is on top, so
  // a 401 never silently discards the form underneath it.
  import Modal from "$lib/components/atoms/Modal.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { Save } from "lucide";

  let {
    open = false,
    title = "",
    ariaLabel = "",
    error = "",
    submitting = false,
    submitDisabled = false,
    submitLabel = "Save",
    submittingLabel = "Saving...",
    submitIcon = Save,
    cancel = true,
    dialogClass = "",
    novalidate = false,
    canClose = () => true,
    onclose,
    onsubmit,
    header,
    headerHint,
    children,
    extraActions,
  } = $props();

  function onModalClose() {
    if (auth.dialogOpen) return;
    if (!canClose()) return;
    onclose?.();
  }
</script>

<Modal {open} variant="editor" onclose={onModalClose}>
  <div
    class={["model-editor", dialogClass]}
    role="dialog"
    aria-modal="true"
    aria-label={ariaLabel || title}
  >
    <form
      class="form"
      {novalidate}
      onsubmit={(event) => {
        event.preventDefault();
        onsubmit?.();
      }}
    >
      <div class="editor-header">
        <div>
          {#if header}
            {@render header()}
          {:else}
            <h3>{title}</h3>
            {#if headerHint}
              {@render headerHint()}
            {/if}
          {/if}
        </div>
        <DialogCloseButton
          label={"Close " + (ariaLabel || title).toLowerCase()}
          onclick={() => onclose?.()}
        />
      </div>

      {@render children?.()}

      {#if error}
        <p class="form-error" role="alert" aria-live="assertive">{error}</p>
      {/if}
      <div class="form-actions">
        {#if cancel}
          <button type="button" class="btn" onclick={() => onclose?.()}>
            Cancel
          </button>
        {/if}
        {#if extraActions}
          {@render extraActions()}
        {/if}
        <button
          type="submit"
          class="btn btn-primary btn-with-icon"
          disabled={submitting || submitDisabled}
        >
          <Icon icon={submitIcon} class="form-action-icon" />
          <span>{submitting ? submittingLabel : submitLabel}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>
