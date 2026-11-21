<script>
  // "Send a test prompt" action of a model/alias row. Renders nothing for
  // rows a chat prompt cannot exercise (see rowCanTestModel).
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { Play } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { modelTestState } from "./modelTest.svelte.js";
  import { rowCanTestModel } from "./modelTest.js";

  let { row } = $props();

  const running = $derived(modelTestState.isRunning(row));
  const label = $derived(
    running
      ? m.models_testing_model({ name: row.display_name })
      : m.models_test_model({ name: row.display_name }),
  );
</script>

{#if rowCanTestModel(row)}
  <TableActionButton
    {label}
    class="table-icon-btn"
    onclick={() => modelTestState.run(row)}
    disabled={running}
  >
    {#if running}
      <Spinner size={14} {label} />
    {:else}
      <Icon icon={Play} class="table-icon-svg" />
    {/if}
  </TableActionButton>
{/if}
