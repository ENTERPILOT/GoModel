<script>
  // Tool picker for the MCP server editor: one checkbox per tool the server
  // reports, plus the policy for tools it adds later. "Expose automatically"
  // saves unchecked tools as disallowed_tools; "Keep hidden" saves checked
  // tools as allowed_tools. Listed names the server does not report stay
  // visible (with a remove action) so typos and removed tools never linger
  // unseen, and names can be added before the server connects.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    MCP_TOOL_MODE_ALLOW,
    MCP_TOOL_MODE_EXCLUDE,
    mcpToolModeSwitchable,
    mcpToolPickerRows,
    mcpToolSelectionSummary,
  } from "./mcp-servers.js";
  import { Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const form = $derived(mcpServers.form);
  const discovered = $derived(mcpServers.editorTools.tools);
  const allowMode = $derived(form.tool_mode === MCP_TOOL_MODE_ALLOW);
  const rows = $derived(mcpToolPickerRows(form, discovered, mcpServers.toolQuery));
  const summary = $derived(mcpToolSelectionSummary(form, discovered));
  const modeLocked = $derived(!mcpToolModeSwitchable(form, discovered));
  const visibleDiscovered = $derived(
    rows.filter((row) => !row.missing).map((row) => row.name),
  );
  const modeOptions = $derived([
    { value: MCP_TOOL_MODE_EXCLUDE, label: m.mcp_tools_mode_exclude() },
    { value: MCP_TOOL_MODE_ALLOW, label: m.mcp_tools_mode_allow() },
  ]);

  function onDraftKeydown(event) {
    // Enter adds the name instead of submitting the whole editor form.
    if (event.key === "Enter") {
      event.preventDefault();
      mcpServers.addToolDraft();
    }
  }
</script>

<section class="mcp-tools" aria-labelledby="mcp-tools-title">
  <div class="mcp-tools-header">
    <span id="mcp-tools-title" class="form-field-label">{m.mcp_tools_title()}</span>
    {#if summary.total > 0}
      <span class="mcp-tools-summary">{m.mcp_tools_summary(summary)}</span>
    {/if}
  </div>

  <div class="mcp-tools-mode">
    <span class="mcp-tools-mode-label">{m.mcp_tools_mode_label()}</span>
    <SegmentedControl
      options={modeOptions}
      value={form.tool_mode}
      ariaLabel={m.mcp_tools_mode_label()}
      disabled={modeLocked}
      onchange={(mode) => mcpServers.switchToolMode(mode)}
    />
  </div>
  <small class="form-hint">
    {allowMode ? m.mcp_tools_mode_allow_help() : m.mcp_tools_mode_exclude_help()}
    {#if modeLocked}
      {m.mcp_tools_mode_locked()}
    {/if}
  </small>

  {#if mcpServers.editorTools.loading}
    <LoadingState label={m.mcp_tools_loading()} class="mcp-tools-loading" />
  {:else if discovered.length === 0}
    <p class="form-hint mcp-tools-empty" title={mcpServers.editorTools.error}>
      {mcpServers.editorTools.error ? m.mcp_tools_load_failed() : m.mcp_tools_pending()}
    </p>
  {:else}
    <div class="mcp-tools-toolbar">
      <FilterInput
        bind:value={mcpServers.toolQuery}
        placeholder={m.mcp_tools_filter()}
        label={m.mcp_tools_filter()}
        onclear={() => (mcpServers.toolQuery = "")}
      />
      <button
        type="button"
        class="btn"
        disabled={visibleDiscovered.length === 0}
        onclick={() => mcpServers.setToolsExposed(visibleDiscovered, true)}
        >{m.mcp_tools_expose_all()}</button
      >
      <button
        type="button"
        class="btn"
        disabled={visibleDiscovered.length === 0}
        onclick={() => mcpServers.setToolsExposed(visibleDiscovered, false)}
        >{m.mcp_tools_hide_all()}</button
      >
    </div>
  {/if}

  {#if rows.length > 0}
    <ul class="mcp-tools-list">
      {#each rows as row (row.name)}
        <li class="mcp-tools-row" class:mcp-tools-row-hidden={!row.exposed}>
          {#if row.missing}
            <span class="mcp-tools-copy">
              <code class="mcp-tools-name mono">{row.name}</code>
              <span class="mcp-tools-description">{m.mcp_tools_missing()}</span>
            </span>
            <TableActionButton
              label={m.mcp_tools_remove({ name: row.name })}
              class="table-action-btn-danger table-icon-btn"
              onclick={() => mcpServers.removeToolName(row.name)}
            >
              <Icon icon={Trash2} class="table-icon-svg" />
            </TableActionButton>
          {:else}
            <label class="mcp-tools-toggle">
              <input
                type="checkbox"
                checked={row.exposed}
                aria-label={m.mcp_tools_toggle({ name: row.name })}
                onchange={(event) =>
                  mcpServers.setToolsExposed([row.name], event.currentTarget.checked)}
              />
              <span class="mcp-tools-copy">
                <span class="mcp-tools-name-line">
                  <code class="mcp-tools-name mono">{row.name}</code>
                  {#if row.readOnly}
                    <span class="mcp-tools-badge" title={m.mcp_tools_hint_title()}
                      >{m.mcp_tools_read_only()}</span
                    >
                  {/if}
                  {#if row.destructive}
                    <span
                      class="mcp-tools-badge mcp-tools-badge-danger"
                      title={m.mcp_tools_hint_title()}>{m.mcp_tools_destructive()}</span
                    >
                  {/if}
                </span>
                {#if row.description}
                  <span class="mcp-tools-description">{row.description}</span>
                {/if}
              </span>
            </label>
          {/if}
        </li>
      {/each}
    </ul>
  {:else if discovered.length > 0}
    <p class="form-hint mcp-tools-empty">{m.mcp_tools_no_match()}</p>
  {/if}

  <div class="mcp-tools-add">
    <input
      type="text"
      class="mono"
      placeholder={m.mcp_tools_add_placeholder()}
      aria-label={m.mcp_tools_add_label()}
      bind:value={mcpServers.toolDraft}
      onkeydown={onDraftKeydown}
    />
    <button
      type="button"
      class="btn"
      disabled={!mcpServers.toolDraft.trim()}
      onclick={() => mcpServers.addToolDraft()}
      >{allowMode ? m.mcp_tools_add_allow() : m.mcp_tools_add_exclude()}</button
    >
  </div>
  <small class="form-hint">{m.mcp_tools_add_help()}</small>
</section>

<style>
  /* MCP tool picker: exposure checkboxes with a scrollable list so servers
     with many tools (GitHub reports ~90) keep the editor compact. */
  .mcp-tools {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .mcp-tools-header,
  .mcp-tools-mode {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }

  .mcp-tools-summary,
  .mcp-tools-mode-label {
    color: var(--text-muted);
    font-size: 13px;
  }

  .mcp-tools-summary {
    font-variant-numeric: tabular-nums;
  }

  .mcp-tools-toolbar,
  .mcp-tools-add {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .mcp-tools-toolbar :global(.filter-input-wrap) {
    min-width: 0;
  }

  .mcp-tools-toolbar .btn {
    white-space: nowrap;
  }

  .mcp-tools-add input {
    flex: 1;
    min-width: 0;
  }

  .mcp-tools-empty {
    margin: 0;
  }

  .mcp-tools-list {
    max-height: 320px;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    list-style: none;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .mcp-tools-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 10px;
  }

  .mcp-tools-row + .mcp-tools-row {
    border-top: 1px solid var(--border);
  }

  .mcp-tools-toggle {
    display: flex;
    flex: 1;
    align-items: flex-start;
    gap: 10px;
    min-width: 0;
    cursor: pointer;
  }

  .mcp-tools-toggle input {
    flex: 0 0 auto;
    margin-top: 2px;
  }

  .mcp-tools-copy {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .mcp-tools-name-line {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
  }

  .mcp-tools-name {
    font-size: 13px;
    overflow-wrap: anywhere;
  }

  .mcp-tools-row-hidden .mcp-tools-name {
    color: var(--text-muted);
    text-decoration: line-through;
  }

  .mcp-tools-description {
    color: var(--text-muted);
    font-size: 12px;
    display: -webkit-box;
    overflow: hidden;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }

  .mcp-tools-badge {
    display: inline-block;
    padding: 0 6px;
    border: 1px solid var(--border);
    border-radius: 999px;
    color: var(--text-muted);
    font-size: 11px;
    line-height: 18px;
  }

  .mcp-tools-badge-danger {
    border-color: color-mix(in srgb, var(--danger) 35%, var(--border));
    color: var(--danger);
  }

  @media (max-width: 560px) {
    .mcp-tools-toolbar {
      flex-wrap: wrap;
    }
  }
</style>
