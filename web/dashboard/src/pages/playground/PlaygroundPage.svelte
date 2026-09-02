<script>
  // Playground: try any model through the gateway's public API. Endpoint and
  // model selection plus "add message" controls sit on top, the editable
  // conversation in the middle, the composer at the bottom, and a slidable
  // request/response JSON panel on the right.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { sortableList } from "$lib/utils/sortable.js";
  import { Braces } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import PlaygroundToolbar from "./PlaygroundToolbar.svelte";
  import PlaygroundMessage from "./PlaygroundMessage.svelte";
  import PlaygroundComposer from "./PlaygroundComposer.svelte";
  import PlaygroundJsonPanel from "./PlaygroundJsonPanel.svelte";
  import { playgroundStore as store } from "./playground.svelte.js";
  import { playgroundModelOptions, defaultUserPathForModel } from "./playgroundLogic.js";

  const PAGE = "playground";

  const modelOptions = $derived(playgroundModelOptions(modelsStore.models));

  // Pick the first inventory model once, when nothing has been chosen yet.
  // If a model is already set (restored from storage), prefill the user path
  // with its first allowed path when one exists and none is chosen yet.
  // Re-runs when store.userPath changes; Svelte bails on identical assignment,
  // so this does not loop.
  $effect(() => {
    if (router.page !== PAGE || modelOptions.length === 0) return;
    if (!store.model) {
      store.setModel(modelOptions[0].id);
    } else if (!store.userPath) {
      store.setUserPath(defaultUserPathForModel(modelsStore.models, store.model));
    }
  });

  let historyEl = $state(null);

  // Keep the newest message in view while it streams in.
  $effect(() => {
    const last = store.messages[store.messages.length - 1];
    void last?.content;
    if (!historyEl || !last?.pending) return;
    historyEl.scrollTop = historyEl.scrollHeight;
  });
</script>

<div class="playground" class:playground-panel-open={store.panelOpen}>
  <section class="playground-main" aria-label={m.playground_title()}>
    <div class="page-header playground-header">
      <h2>{m.playground_title()}</h2>
      <div class="page-header-controls">
        <button
          type="button"
          class="btn btn-with-icon"
          aria-pressed={store.panelOpen}
          aria-controls="playground-json-panel"
          onclick={() => store.togglePanel()}
        >
          <Icon icon={Braces} class="table-icon-svg" />
          <span>{store.panelOpen ? m.playground_json_hide() : m.playground_json_show()}</span>
        </button>
      </div>
    </div>

    <PlaygroundToolbar {modelOptions} />

    {#if store.error}
      <div class="alert alert-warning playground-error" role="alert">{store.error}</div>
    {/if}

    <div
      class="playground-history"
      bind:this={historyEl}
      {@attach sortableList({ onreorder: (from, to) => store.moveMessage(from, to) })}
    >
      {#if store.messages.length === 0}
        <p class="empty-state">{m.playground_empty()}</p>
      {:else}
        {#each store.messages as message (message.id)}
          <PlaygroundMessage {message} />
        {/each}
      {/if}
    </div>

    <PlaygroundComposer />
  </section>

  <PlaygroundJsonPanel />
</div>

<style>
  .playground {
    display: flex;
    align-items: stretch;
    gap: 16px;
    height: 100%;
    min-height: 480px;
  }

  .playground-main {
    display: flex;
    flex: 1 1 0;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }

  .playground-header {
    margin-bottom: 16px;
  }

  .playground-error {
    margin-bottom: 12px;
  }

  .playground-history {
    display: flex;
    flex: 1 1 0;
    flex-direction: column;
    gap: 10px;
    min-height: 0;
    padding: 4px 2px 12px;
    overflow-y: auto;
    scroll-behavior: smooth;
  }

  /* Dragged rows translate over their siblings; scrolling mid-drag would
     desync the cached midpoints. */
  .playground-history:global(.is-sorting) {
    overflow-y: hidden;
    user-select: none;
  }

  .playground-history .empty-state {
    margin: auto;
    max-width: 420px;
  }

  @media (max-width: 768px) {
    .playground {
      min-height: 0;
    }
  }
</style>
