// Client for the gateway's SSE-framed admin streams (GET /admin/live/logs).
//
// Two consumers read that endpoint — the audit-log live stream
// (pages/audit-logs/live-logs-logic.js) and the overview's token-throughput
// signal stream (pages/overview/liveTokensState.svelte.js) — and each used to
// carry its own copy of the framing, the `data:` parsing and the reconnect
// backoff. The transport lives here once; the consumers keep their own
// policy (what to do with an event, when reconnecting is allowed).
//
// No imports, no runes: `live-logs-logic.js` is loaded directly by node --test,
// which cannot resolve the `$lib` alias, so it imports this module by relative
// path while bundled code uses `$lib/api/eventStream.js`.

/**
 * Ceiling for the attempt counter, NOT a retry limit: the consumers keep
 * reconnecting indefinitely, and once the counter pins here the delay stops
 * growing (see nextReconnect).
 */
export const MAX_RECONNECT_ATTEMPTS = 6;

/**
 * Read an SSE body to completion, decoding each frame's `data:` lines and
 * handing the parsed JSON to `onEvent`. Frames that are not valid JSON are
 * skipped; a trailing frame with no blank-line terminator is still flushed.
 *
 * @param {{read: () => Promise<{done: boolean, value?: Uint8Array}>}} reader
 * @param {(event: unknown) => void} onEvent
 */
export async function consumeEventStream(reader, onEvent) {
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const chunk = await reader.read();
    if (chunk.done) break;
    buffer += decoder.decode(chunk.value, { stream: true });
    let delimiter;
    while ((delimiter = buffer.match(/\r?\n\r?\n/))) {
      const splitAt = delimiter.index;
      const frame = buffer.slice(0, splitAt);
      buffer = buffer.slice(splitAt + delimiter[0].length);
      emitFrame(frame, onEvent);
    }
  }
  buffer += decoder.decode();
  if (buffer.trim()) {
    emitFrame(buffer, onEvent);
  }
}

function emitFrame(frame, onEvent) {
  const lines = String(frame || "").split(/\r?\n/);
  const data = [];
  for (const line of lines) {
    if (line.indexOf("data:") === 0) {
      data.push(line.slice(5).trimStart());
    }
  }
  if (data.length === 0) return;
  let event;
  try {
    event = JSON.parse(data.join("\n"));
  } catch {
    return;
  }
  onEvent(event);
}

/**
 * Next attempt number and its exponential backoff delay: 500ms doubling per
 * attempt, so the delay tops out at 16s once `attempt` reaches
 * MAX_RECONNECT_ATTEMPTS (the 30s ceiling below is a guard the attempt cap
 * keeps unreachable). `attempts` is the count of consecutive failures so far.
 *
 * @param {number} attempts
 * @returns {{attempt: number, delay: number}}
 */
export function nextReconnect(attempts) {
  const attempt = Math.min(Number(attempts || 0) + 1, MAX_RECONNECT_ATTEMPTS);
  return { attempt, delay: Math.min(30000, 500 * Math.pow(2, attempt - 1)) };
}
