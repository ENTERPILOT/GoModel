<script>
  // One session thread in grouped mode: the latest request as the head row
  // (with an expander + count badge) and, once unfolded, the session's older
  // requests indented beneath it with L-shaped connector lines. Entries that
  // are alone in their session render as a plain row.
  import { slide } from "svelte/transition";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { motionDuration } from "$lib/utils/motion.ts";
  import AuditEntryRow from "./AuditEntryRow.svelte";
  import { auditList } from "./auditList.svelte.js";
  import {
    auditIsThreadHead,
    auditSessionCount,
    auditSessionId,
  } from "./audit-logic.js";

  let { entry } = $props();

  const sessionId = $derived(auditSessionId(entry));
  const expanded = $derived(auditList.isThreadExpanded(sessionId));
  const children = $derived(auditList.threadChildren(sessionId));
  const truncated = $derived(
    children &&
      !children.loading &&
      Number(children.total || 0) > children.entries.length + 1,
  );
</script>

{#if !auditIsThreadHead(entry)}
  <AuditEntryRow {entry} />
{:else}
  <div class="audit-thread">
    <AuditEntryRow
      {entry}
      thread={{
        count: auditSessionCount(entry),
        expanded,
        ontoggle: () => auditList.toggleThread(entry),
      }}
    />
    {#if expanded}
      <div
        class="audit-thread-children"
        transition:slide={{ duration: motionDuration(150) }}
      >
        {#if children && children.loading}
          <div class="audit-thread-loading">
            <Spinner size={14} label="Loading session requests" />
          </div>
        {/if}
        {#each (children && children.entries) || [] as child (child.id)}
          <div class="audit-thread-child">
            <AuditEntryRow entry={child} />
          </div>
        {/each}
        {#if truncated}
          <p class="audit-thread-more">
            Showing the latest {children.entries.length + 1} of {children.total}
            requests in this session.
          </p>
        {/if}
      </div>
    {/if}
  </div>
{/if}

<style>
  .audit-thread {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .audit-thread-children {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-left: 26px;
  }

  .audit-thread-child,
  .audit-thread-loading {
    position: relative;
  }

  /* The L: a vertical drop from the row above with a rounded arm into the
     child row. Negative top offset bridges the 10px flex gap. */
  .audit-thread-child::before,
  .audit-thread-loading::before {
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
  .audit-thread-child:not(:last-child)::after {
    content: "";
    position: absolute;
    left: -14px;
    top: 50%;
    bottom: -10px;
    border-left: 1px solid var(--border);
    pointer-events: none;
  }

  .audit-thread-loading {
    display: flex;
    padding: 6px 0 6px 4px;
  }

  .audit-thread-more {
    color: var(--text-muted);
    font-size: 12px;
    padding-left: 4px;
  }
</style>
