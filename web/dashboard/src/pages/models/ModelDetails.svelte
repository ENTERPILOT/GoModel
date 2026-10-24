<script>
  // The details row under an expanded model row: what the provider listed,
  // the model's properties, capabilities, rankings and pricing. The panel
  // opens on the effective metadata the inventory row already carries, then
  // fetches the layers behind it (/admin/models/metadata) to label each
  // field's source and offer the provider / catalog / config views.
  // Rendered by ModelRow while the row is expanded; `id` is what the
  // toggle's aria-controls points at.
  import { untrack } from "svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import { modelDetailsState } from "./modelDetails.svelte.js";
  import {
    buildModelDetails,
    metadataViewEmptyMessage,
    metadataViewOptions,
    resolveMetadataView,
  } from "./modelDetails.js";
  import * as m from "$lib/paraglide/messages.js";

  let { row, colspan, id } = $props();

  $effect(() => {
    const current = row;
    untrack(() => modelDetailsState.loadLayers(current));
  });

  const entry = $derived(modelDetailsState.layersEntry(row));
  const layers = $derived(entry && entry.status === "ok" ? entry.layers : null);
  const viewOptions = $derived(metadataViewOptions(layers));
  const view = $derived(resolveMetadataView(modelDetailsState.view, layers));
  const details = $derived(
    buildModelDetails(
      row,
      pricingOverrides.modelRowPricing(row),
      pricingOverrides.modelRowPricingSources(row),
      { view, layers },
    ),
  );
</script>

<tr class="model-details-row" {id}>
  <td colspan={colspan}>
    {#if viewOptions.length > 0}
      <div class="model-details-toolbar">
        <SegmentedControl
          options={viewOptions}
          value={view}
          ariaLabel={m.models_details_view_label()}
          onchange={(next) => (modelDetailsState.view = next)}
        />
      </div>
    {:else if entry && entry.status === "loading"}
      <p class="model-details-note">{m.models_details_layers_loading()}</p>
    {:else if entry && entry.status === "error"}
      <p class="model-details-note">{m.models_details_layers_failed()}</p>
    {/if}
    {#if !details.has_metadata}
      <p class="model-details-note">{metadataViewEmptyMessage(view)}</p>
    {/if}
    <div class="model-details">
      {#each details.sections as section (section.key)}
        <section class="model-details-section">
          <h4 class="model-details-title">{section.title}</h4>
          {#if section.chips}
            <ul class="model-details-chips">
              {#each section.chips as chip (chip.label)}
                {@const chipStatus = [chip.enabled ? m.models_details_supported() : m.models_details_unsupported(), chip.source]
                  .filter(Boolean)
                  .join(" · ")}
                <li class="provider-badge model-details-chip" class:is-off={!chip.enabled} title={chipStatus}>
                  {chip.label}<span class="model-details-sr-only">, {chipStatus}</span>
                </li>
              {/each}
            </ul>
          {:else}
            <dl class="model-details-list">
              {#each section.items as field (field.label)}
                <dt>{field.label}</dt>
                <dd class:mono={field.mono} title={field.hint || undefined}>
                  {field.value}{#if field.hint}<span class="model-details-hint" aria-hidden="true">*</span><span class="model-details-sr-only">, {field.hint}</span>{/if}
                  {#if field.source}
                    <span class="model-details-source">({field.source})</span>
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

  .model-details-toolbar {
    display: flex;
    align-items: center;
    margin-bottom: 12px;
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

  /* Text for assistive technology only: the chip's status and a price's
     time-window hint are otherwise carried by non-focusable title tooltips. */
  .model-details-sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    border: 0;
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
