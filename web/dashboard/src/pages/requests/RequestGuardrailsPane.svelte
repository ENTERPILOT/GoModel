<script>
  // The Guardrails pane of an expanded request log entry: one row per guardrail
  // instance that ran, in execution order, built by requestGuardrailsPane in
  // request-log-logic.js. Rows carrying a plugin detail or an error expand into a
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

<section class="request-log-pane request-log-guardrails-pane">
  <div class="request-log-guardrails-scroll">
    <table class="request-log-guardrails-table" aria-label={m.requests_guardrail_table_label()}>
      <thead>
        <tr>
          <th class="request-log-guardrails-toggle-col"><span class="request-log-guardrails-sr-only">{m.requests_guardrail_detail_title()}</span></th>
          <th>{m.requests_guardrail_col_phase()}</th>
          <th class="request-log-guardrails-num">{m.requests_guardrail_col_step()}</th>
          <th>{m.requests_guardrail_col_instance()}</th>
          <th>{m.requests_guardrail_col_action()}</th>
          <th>{m.requests_guardrail_col_edited()}</th>
          <th>{m.requests_guardrail_col_result()}</th>
          <th>{m.requests_guardrail_col_fail_mode()}</th>
          <th class="request-log-guardrails-num">{m.requests_guardrail_col_duration()}</th>
        </tr>
      </thead>
      <tbody>
        {#each pane.rows as row (row.id)}
          {@const open = openRows.has(row.id)}
          <tr class:request-log-guardrails-row-open={open}>
            <td class="request-log-guardrails-toggle-col">
              {#if expandable(row)}
                <button
                  type="button"
                  class="request-log-guardrails-toggle"
                  aria-expanded={open}
                  aria-controls={"request-log-guardrail-detail-" + row.id}
                  title={open ? m.requests_guardrail_hide_detail() : m.requests_guardrail_show_detail()}
                  aria-label={open ? m.requests_guardrail_hide_detail() : m.requests_guardrail_show_detail()}
                  onclick={() => toggleRow(row.id)}
                >
                  <Icon icon={open ? ChevronDown : ChevronRight} />
                </button>
              {/if}
            </td>
            <td>{row.phase}</td>
            <td class="request-log-guardrails-num mono">{row.step}</td>
            <td>
              <span class="mono">{row.instance}</span>
              {#if row.type}
                <span class="request-log-guardrails-type mono">{row.type}</span>
              {/if}
            </td>
            <td>
              <span class="request-log-status-badge {row.actionClass}">{row.actionLabel}</span>
            </td>
            <td class="request-log-guardrails-muted">{row.edited}</td>
            <td class="request-log-guardrails-result">
              {#if row.code}
                <span class="mono">{row.code}</span>
              {/if}
              {#if row.code && row.message}
                <span class="request-log-guardrails-muted" aria-hidden="true"> · </span>
              {/if}
              {#if row.message}
                <span>{row.message}</span>
              {/if}
              {#if row.streamEvents}
                <span class="request-log-guardrails-muted request-log-guardrails-stream">{row.streamEvents}</span>
              {/if}
            </td>
            <td class="request-log-guardrails-muted">{row.failMode}</td>
            <td class="request-log-guardrails-num mono">{row.duration}</td>
          </tr>
          {#if open && expandable(row)}
            <tr class="request-log-guardrails-detail-row">
              <td colspan="9" id={"request-log-guardrail-detail-" + row.id}>
                {#if row.error}
                  <div class="request-log-guardrails-block">
                    <h5>{m.requests_guardrail_error_title()}</h5>
                    <pre class="request-log-guardrails-json request-log-guardrails-error">{row.error}</pre>
                  </div>
                {/if}
                {#if row.detail}
                  <div class="request-log-guardrails-block">
                    <h5>{m.requests_guardrail_detail_title()}</h5>
                    <pre class="request-log-guardrails-json">{row.detail}</pre>
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
  .request-log-guardrails-scroll {
    max-width: 100%;
    overflow-x: auto;
  }

  .request-log-guardrails-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }

  .request-log-guardrails-table th,
  .request-log-guardrails-table td {
    padding: 6px 8px;
    text-align: left;
    vertical-align: middle;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }

  .request-log-guardrails-table th {
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .request-log-guardrails-table tbody tr:last-child td {
    border-bottom: 0;
  }

  .request-log-guardrails-num {
    text-align: right;
  }

  .request-log-guardrails-table th.request-log-guardrails-num {
    text-align: right;
  }

  .request-log-guardrails-toggle-col {
    width: 28px;
    padding-right: 0;
  }

  .request-log-guardrails-toggle {
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

  .request-log-guardrails-toggle:hover {
    color: var(--text);
    border-color: color-mix(in srgb, var(--border) 60%, var(--text) 40%);
  }

  .request-log-guardrails-toggle :global(svg) {
    width: 14px;
    height: 14px;
  }

  .request-log-guardrails-type {
    margin-left: 6px;
    color: var(--text-muted);
    font-size: 11px;
  }

  .request-log-guardrails-muted {
    color: var(--text-muted);
  }

  /* The code · message cell is the only one allowed to wrap. */
  .request-log-guardrails-table td.request-log-guardrails-result {
    white-space: normal;
    min-width: 200px;
    max-width: 480px;
  }

  .request-log-guardrails-stream {
    display: block;
    font-size: 11px;
  }

  .request-log-guardrails-detail-row td {
    padding: 8px 10px 10px 36px;
    background: var(--bg-surface);
  }

  .request-log-guardrails-block + .request-log-guardrails-block {
    margin-top: 8px;
  }

  .request-log-guardrails-block > h5 {
    margin-bottom: 6px;
  }

  .request-log-guardrails-json {
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

  .request-log-guardrails-error {
    color: var(--danger);
    white-space: pre-wrap;
  }

  .request-log-guardrails-sr-only {
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
