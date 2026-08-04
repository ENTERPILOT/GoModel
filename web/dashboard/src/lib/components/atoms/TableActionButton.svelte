<script>
  // Small action button used in table rows, cards, and editors
  // (.table-action-btn styles). `label` fills both title and aria-label.
  // Children supply the icon (and optional text span), e.g.
  //   <TableActionButton label="Edit budget" class="budget-action-btn" onclick={...}>
  //     <Icon name="pencil" class="budget-action-icon" />
  //     <span class="budget-action-label">Edit</span>
  //   </TableActionButton>
  let {
    label = "",
    onclick,
    class: className = "",
    disabled = false,
    children,
  } = $props();
</script>

<button
  type="button"
  class={["table-action-btn", className]}
  aria-label={label}
  title={label}
  {disabled}
  {onclick}
>
  {@render children?.()}
</button>
