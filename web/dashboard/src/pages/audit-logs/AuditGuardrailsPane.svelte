<script>
  // The Guardrails pane of an expanded audit entry: one row per guardrail
  // instance that ran, in execution order, built by auditGuardrailsPane in
  // audit-logic.js. Rows carrying a plugin detail or an error expand into a
  // pre-formatted block underneath.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { ChevronDown, ChevronRight } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let { pane } = $props();

  // Ids of the rows whose detail block is open.
  let openRows = $state(new Set());

  function toggleRow(id) {
    const next = new Set(openRows);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    openRows = next;
  }

  function expandable(row) {
    return !!(row.detail || row.error);
  }
</script>

<section class="audit-pane audit-guardrails-pane">
  <div class="audit-guardrails-scroll">
    <table class="audit-guardrails-table" aria-label={m.audit_guardrail_table_label()}>
      <thead>
        <tr>
          <th class="audit-guardrails-toggle-col"><span class="audit-guardrails-sr-only">{m.audit_guardrail_detail_title()}</span></th>
          <th>{m.audit_guardrail_col_phase()}</th>
          <th class="audit-guardrails-num">{m.audit_guardrail_col_step()}</th>
          <th>{m.audit_guardrail_col_instance()}</th>
          <th>{m.audit_guardrail_col_action()}</th>
          <th>{m.audit_guardrail_col_edited()}</th>
          <th>{m.audit_guardrail_col_result()}</th>
          <th>{m.audit_guardrail_col_fail_mode()}</th>
          <th class="audit-guardrails-num">{m.audit_guardrail_col_duration()}</th>
        </tr>
      </thead>
      <tbody>
        {#each pane.rows as row (row.id)}
          {@const open = openRows.has(row.id)}
          <tr class:audit-guardrails-row-open={open}>
            <td class="audit-guardrails-toggle-col">
              {#if expandable(row)}
                <button
                  type="button"
                  class="audit-guardrails-toggle"
                  aria-expanded={open}
                  aria-controls={"audit-guardrail-detail-" + row.id}
                  title={open ? m.audit_guardrail_hide_detail() : m.audit_guardrail_show_detail()}
                  aria-label={open ? m.audit_guardrail_hide_detail() : m.audit_guardrail_show_detail()}
                  onclick={() => toggleRow(row.id)}
                >
                  <Icon icon={open ? ChevronDown : ChevronRight} />
                </button>
              {/if}
            </td>
            <td>{row.phase}</td>
            <td class="audit-guardrails-num mono">{row.step}</td>
            <td>
              <span class="mono">{row.instance}</span>
              {#if row.type}
                <span class="audit-guardrails-type mono">{row.type}</span>
              {/if}
            </td>
            <td>
              <span class="audit-status-badge {row.actionClass}">{row.actionLabel}</span>
            </td>
            <td class="audit-guardrails-muted">{row.edited}</td>
            <td class="audit-guardrails-result">
              {#if row.code}
                <span class="mono">{row.code}</span>
              {/if}
              {#if row.code && row.message}
                <span class="audit-guardrails-muted" aria-hidden="true"> · </span>
              {/if}
              {#if row.message}
                <span>{row.message}</span>
              {/if}
              {#if row.streamEvents}
                <span class="audit-guardrails-muted audit-guardrails-stream">{row.streamEvents}</span>
              {/if}
            </td>
            <td class="audit-guardrails-muted">{row.failMode}</td>
            <td class="audit-guardrails-num mono">{row.duration}</td>
          </tr>
          {#if open && expandable(row)}
            <tr class="audit-guardrails-detail-row">
              <td colspan="9" id={"audit-guardrail-detail-" + row.id}>
                {#if row.error}
                  <div class="audit-guardrails-block">
                    <h5>{m.audit_guardrail_error_title()}</h5>
                    <pre class="audit-guardrails-json audit-guardrails-error">{row.error}</pre>
                  </div>
                {/if}
                {#if row.detail}
                  <div class="audit-guardrails-block">
                    <h5>{m.audit_guardrail_detail_title()}</h5>
                    <pre class="audit-guardrails-json">{row.detail}</pre>
                  </div>
                {/if}
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
</section>

<style>
  .audit-guardrails-scroll {
    max-width: 100%;
    overflow-x: auto;
  }

  .audit-guardrails-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }

  .audit-guardrails-table th,
  .audit-guardrails-table td {
    padding: 6px 8px;
    text-align: left;
    vertical-align: middle;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }

  .audit-guardrails-table th {
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .audit-guardrails-table tbody tr:last-child td {
    border-bottom: 0;
  }

  .audit-guardrails-num {
    text-align: right;
  }

  .audit-guardrails-table th.audit-guardrails-num {
    text-align: right;
  }

  .audit-guardrails-toggle-col {
    width: 28px;
    padding-right: 0;
  }

  .audit-guardrails-toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-surface);
    color: var(--text-muted);
    cursor: pointer;
  }

  .audit-guardrails-toggle:hover {
    color: var(--text);
    border-color: color-mix(in srgb, var(--border) 60%, var(--text) 40%);
  }

  .audit-guardrails-toggle :global(svg) {
    width: 14px;
    height: 14px;
  }

  .audit-guardrails-type {
    margin-left: 6px;
    color: var(--text-muted);
    font-size: 11px;
  }

  .audit-guardrails-muted {
    color: var(--text-muted);
  }

  /* The code · message cell is the only one allowed to wrap. */
  .audit-guardrails-table td.audit-guardrails-result {
    white-space: normal;
    min-width: 200px;
    max-width: 480px;
  }

  .audit-guardrails-stream {
    display: block;
    font-size: 11px;
  }

  .audit-guardrails-detail-row td {
    padding: 8px 10px 10px 36px;
    background: var(--bg-surface);
  }

  .audit-guardrails-block + .audit-guardrails-block {
    margin-top: 8px;
  }

  .audit-guardrails-block > h5 {
    margin-bottom: 6px;
  }

  .audit-guardrails-json {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    box-sizing: border-box;
    font-family: "SF Mono", Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.45;
    max-width: 100%;
    padding: 10px;
    max-height: 220px;
    overflow: auto;
    white-space: pre;
    color: var(--text);
  }

  .audit-guardrails-error {
    color: var(--danger);
    white-space: pre-wrap;
  }

  .audit-guardrails-sr-only {
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
</style>
