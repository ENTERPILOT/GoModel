<script>
  // FormField — the label + control wrapper every editor form-grid cell uses.
  // The control itself stays in the caller (inputs need page-specific
  // bindings, autofocus flags, and datalists), so this only standardizes the
  // .form-field / .form-field-label shell around it.
  //
  // Props: id (the control's id, wired to the label), label, children.
  let { id, label = "", children } = $props();
</script>

<div class="form-field">
  <label class="form-field-label" for={id}>{label}</label>
  {@render children?.()}
</div>
