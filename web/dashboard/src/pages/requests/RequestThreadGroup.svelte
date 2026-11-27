<script>
  // One session thread in grouped mode: the latest request as the head row
  // (with an expander + count badge) and, once unfolded, the session's older
  // requests indented beneath it with L-shaped connector lines. Entries that
  // are alone in their session render as a plain row.
  import { slide } from "svelte/transition";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { motionDuration } from "$lib/utils/motion.js";
  import RequestEntryRow from "./RequestEntryRow.svelte";
  import { requestList } from "./requestList.svelte.js";
  import {
    requestIsThreadHead,
    requestSessionCount,
    requestSessionId,
  } from "./request-log-logic.js";
  import * as m from "$lib/paraglide/messages.js";

  let { entry } = $props();

  const sessionId = $derived(requestSessionId(entry));
  const expanded = $derived(requestList.isThreadExpanded(sessionId));
  const children = $derived(requestList.threadChildren(sessionId));
  const truncated = $derived(
    children &&
      !children.loading &&
      Number(children.total || 0) > children.entries.length + 1,
  );
</script>


{#if !requestIsThreadHead(entry)}
  <RequestEntryRow {entry} />
{:else}
  <div class="request-log-thread">
    <RequestEntryRow
      {entry}
      thread={{
        count: requestSessionCount(entry),
        expanded,
        ontoggle: () => requestList.toggleThread(entry),
      }}
    />
    {#if expanded}
      <div
        class="request-log-thread-children"
        transition:slide={{ duration: motionDuration(150) }}
      >
        {#if children && children.loading}
          <div class="request-log-thread-loading">
            <Spinner size={14} label={m.requests_loading_session_requests()} />
          </div>
        {/if}
        {#each (children && children.entries) || [] as child (child.id)}
          <div class="request-log-thread-child">
            <RequestEntryRow entry={child} hidePath={child.path === entry.path} />
          </div>
        {/each}
        {#if truncated}
          <p class="request-log-thread-more">
            {m.requests_thread_summary({
              shown: children.entries.length + 1,
              total: children.total,
            })}
          </p>
        {/if}
      </div>
    {/if}
  </div>

{/if}

<style>
  .request-log-thread {
    display: flex;
    flex-direction: column;

    gap: 12px;
  }

  .request-log-thread-children {
    display: flex;
    flex-direction: column;

    gap: 12px;
    margin-left: 28px;
  }


  .request-log-thread-child,
  .request-log-thread-loading {
    position: relative;
  }

  /* The L: a vertical drop from the row above with a rounded arm into the
     child row. Negative top offset bridges the 10px flex gap. */
  .request-log-thread-child::before,
  .request-log-thread-loading::before {
    content: "";
    position: absolute;
    left: -14px;
    top: -10px;
    width: 12px;
    height: calc(50% + 10px);
    border-left: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    border-bottom-left-radius: 6px;
    pointer-events: none;
  }

  /* Trunk line continuing to the next sibling. */
  .request-log-thread-child:not(:last-child)::after {
    content: "";
    position: absolute;
    left: -14px;
    top: 50%;
    bottom: -10px;
    border-left: 1px solid var(--border);
    pointer-events: none;
  }

  .request-log-thread-loading {
    display: flex;
    padding: 8px 0 8px 4px;
  }

  .request-log-thread-more {
    color: var(--text-muted);
    font-size: 12px;
    padding-left: 4px;
  }
</style>
