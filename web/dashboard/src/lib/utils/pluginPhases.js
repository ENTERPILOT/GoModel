// Plugin hook phases as the admin API reports them (`phases` on guardrail
// types and instances, `kinds` on GET /admin/plugins) and the subset a
// workflow step can run in. Labels stay here so copy does not ship with the
// gateway; unknown phases render as their raw name.

import * as m from "../paraglide/messages.js";

// WORKFLOW_PHASES is the order steps run in and the order the editor,
// cards, and chart group them.
export const WORKFLOW_PHASES = ["prompt", "response", "stream"];

export const DEFAULT_PHASE = "prompt";

// normalizePhases trims and deduplicates a phase list. A missing or empty
// list means the legacy prompt-only contract.
export function normalizePhases(list) {
  const phases = Array.isArray(list)
    ? list.map((item) => String(item || "").trim().toLowerCase()).filter(Boolean)
    : [];
  return phases.length > 0 ? Array.from(new Set(phases)) : [DEFAULT_PHASE];
}

// normalizeWorkflowPhase coerces a step phase to one a workflow accepts.
export function normalizeWorkflowPhase(value) {
  const phase = String(value || "").trim().toLowerCase();
  return WORKFLOW_PHASES.includes(phase) ? phase : DEFAULT_PHASE;
}

// isWorkflowPhase reports whether a value is a valid step phase as given
// (no defaulting), for payload validation.
export function isWorkflowPhase(value) {
  return WORKFLOW_PHASES.includes(String(value || "").trim().toLowerCase());
}

// phasesSupport reports whether a plugin/instance phase list covers a phase.
export function phasesSupport(list, phase) {
  return normalizePhases(list).includes(normalizeWorkflowPhase(phase));
}

export function phaseLabel(phase) {
  switch (String(phase || "").trim().toLowerCase()) {
    case "request":
      return m.plugins_phase_request();
    case "prompt":
      return m.plugins_phase_prompt();
    case "response":
      return m.plugins_phase_response();
    case "stream":
      return m.plugins_phase_stream();
    case "route":
      return m.plugins_phase_route();
    case "complete":
      return m.plugins_phase_complete();
    default:
      return String(phase || "");
  }
}
