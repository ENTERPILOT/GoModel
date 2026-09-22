<script>
  // The details row under an expanded model row: what the provider listed,
  // the merged catalog properties, capabilities, rankings and the effective
  // pricing with the source of each price. Rendered by ModelRow while the
  // row is expanded; `id` is what the toggle's aria-controls points at.
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import { buildModelDetails } from "./modelDetails.js";
  import * as m from "$lib/paraglide/messages.js";

  let { row, colspan, id } = $props();

  const details = $derived(
    buildModelDetails(
      row,
      pricingOverrides.modelRowPricing(row),
      pricingOverrides.modelRowPricingSources(row),
    ),
  );
</script>

<tr class="model-details-row" {id}>
  <td colspan={colspan}>
    {#if !details.has_metadata}
      <p class="model-details-note">{m.models_details_no_metadata()}</p>
    {/if}
    <div class="model-details">
      {#each details.sections as section (section.key)}
        <section class="model-details-section">
          <h4 class="model-details-title">{section.title}</h4>
          {#if section.chips}
            <ul class="model-details-chips">
              {#each section.chips as chip (chip.label)}
                <li
                  class="provider-badge model-details-chip"
                  class:is-off={!chip.enabled}
                  title={chip.enabled ? m.models_details_supported() : m.models_details_unsupported()}
                >{chip.label}</li>
              {/each}
            </ul>
          {:else}
            <dl class="model-details-list">
              {#each section.items as entry (entry.label)}
                <dt>{entry.label}</dt>
                <dd class:mono={entry.mono} title={entry.hint || undefined}>
                  {entry.value}{#if entry.hint}<span class="model-details-hint" aria-hidden="true">*</span>{/if}
                  {#if entry.source}
                    <span class="model-details-source">({entry.source})</span>
                  {/if}
                </dd>
              {/each}
            </dl>
          {/if}
        </section>
      {/each}
    </div>
  </td>
</tr>

<style>
  /* Both rules so the panel keeps its own surface under the table's
     `.data-table tr:hover td` highlight. */
  .model-details-row :global(td),
  .model-details-row:hover :global(td) {
    background: color-mix(in srgb, var(--bg-surface) 70%, var(--bg));
    padding: 12px var(--space-cell) 16px;
  }

  .model-details-note {
    margin: 0 0 12px;
    font-size: 12px;
    color: var(--text-muted);
  }

  .model-details {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 16px 32px;
  }

  .model-details-section {
    min-width: 0;
  }

  .model-details-title {
    margin: 0 0 8px;
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
  }

  .model-details-list {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 4px 12px;
    margin: 0;
    font-size: 13px;
  }

  .model-details-list dt {
    color: var(--text-muted);
  }

  .model-details-list dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .model-details-hint {
    margin-left: 2px;
    color: var(--text-muted);
  }

  .model-details-source {
    margin-left: 6px;
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .model-details-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .model-details-chip.is-off {
    opacity: 0.55;
    border-style: dashed;
  }

  @media (max-width: 768px) {
    .model-details-list {
      grid-template-columns: minmax(0, 1fr);
    }

    .model-details-list dt {
      margin-top: 4px;
    }
  }
</style>
