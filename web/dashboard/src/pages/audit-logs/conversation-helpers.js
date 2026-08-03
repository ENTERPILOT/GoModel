// Pure interaction shaping shared by the Svelte stores and Node tests.

import { formatJSON } from "../../lib/utils/format.js";
import { findNestedErrorMessage, tryParseJSON } from "./error-text.js";

// Re-exported so the drawer store keeps one import for its formatting.
export { formatJSON };

const sectionKeys = new Set(['instructions', 'messages', 'input', 'previous_response_id', 'choices', 'output']);

function extractText(content) {
    if (content == null) return '';
    if (typeof content === 'string') return content.trim();

    if (Array.isArray(content)) {
        const parts = content.map((part) => {
            if (typeof part === 'string') return part;
            if (!part || typeof part !== 'object') return '';
            if (typeof part.text === 'string') return part.text;
            if (typeof part.output_text === 'string') return part.output_text;
            return '';
        }).filter(Boolean);
        return parts.join('\n').trim();
    }

    if (typeof content === 'object') {
        if (typeof content.text === 'string') return content.text.trim();
        try {
            return JSON.stringify(content, null, 2);
        } catch {
            return '';
        }
    }

    return String(content).trim();
}

function extractTextSegments(content) {
    if (content == null) return [];
    if (typeof content === 'string') return content ? [content] : [];

    if (Array.isArray(content)) {
        return content.flatMap((part) => {
            if (typeof part === 'string') return part ? [part] : [];
            if (!part || typeof part !== 'object') return [];
            if (typeof part.text === 'string') return part.text ? [part.text] : [];
            if (typeof part.output_text === 'string') return part.output_text ? [part.output_text] : [];
            return [];
        });
    }

    if (typeof content === 'object') {
        if (typeof content.text === 'string') return content.text ? [content.text] : [];
        return [];
    }

    const text = String(content);
    return text ? [text] : [];
}

function extractResponsesInputMessages(input) {
    if (input == null) return [];
    if (typeof input === 'string') {
        const text = input.trim();
        return text ? [{ role: 'user', text }] : [];
    }

    if (!Array.isArray(input)) {
        const text = extractText(input);
        return text ? [{ role: 'user', text }] : [];
    }

    return input.map((item) => {
        if (!item || typeof item !== 'object') return null;
        const role = String(item.role || 'user').toLowerCase();
        const text = extractText(item.content);
        if (!text) return null;
        return { role, text };
    }).filter(Boolean);
}

function extractResponsesOutputText(item) {
    if (!item || typeof item !== 'object') return '';
    if (!Array.isArray(item.content)) return extractText(item.content);

    const parts = item.content.map((part) => {
        if (!part) return '';
        if (typeof part.text === 'string') return part.text;
        return '';
    }).filter(Boolean);

    return parts.join('\n').trim();
}

export function extractRequestPromptTextSegments(body) {
    if (!body || typeof body !== 'object') return [];

    const segments = [];
    segments.push(...extractTextSegments(body.instructions));

    if (Array.isArray(body.messages)) {
        body.messages.forEach((message) => {
            if (!message || typeof message !== 'object') return;
            segments.push(...extractTextSegments(message.content));
        });
    }

    if (typeof body.input === 'string') {
        segments.push(body.input);
    } else if (Array.isArray(body.input)) {
        body.input.forEach((item) => {
            if (!item || typeof item !== 'object') return;
            segments.push(...extractTextSegments(item.content));
            if (typeof item.text === 'string') {
                segments.push(item.text);
            }
        });
    } else if (body.input && typeof body.input === 'object') {
        segments.push(...extractTextSegments(body.input.content));
        if (typeof body.input.text === 'string') {
            segments.push(body.input.text);
        }
    }

    return segments
        .map((segment) => String(segment || ''))
        .filter((segment) => segment.length > 0);
}

// extractText is the drawer's last resort once the shared walk finds no
// error field: pull whatever readable content the payload carries.
const errorTextFallback = (parsed) => extractText(parsed);

export function extractConversationErrorMessage(entry) {
    if (!entry || !entry.data) return '';

    const responseBodyMessage = findNestedErrorMessage(entry.data.response_body, 0, errorTextFallback);
    if (responseBodyMessage) return responseBodyMessage;

    const rawError = entry.data.error_message;
    if (rawError == null) return '';

    if (typeof rawError === 'string') {
        const trimmed = rawError.trim();
        if (!trimmed) return '';

        const parsed = tryParseJSON(trimmed);
        const parsedMessage = findNestedErrorMessage(parsed, 0, errorTextFallback);
        if (parsedMessage) return parsedMessage;
        return trimmed;
    }

    const structuredMessage = findNestedErrorMessage(rawError, 0, errorTextFallback);
    if (structuredMessage) return structuredMessage;
    return extractText(rawError);
}

function looksLikeResponsesOutput(output) {
    if (!Array.isArray(output)) return false;
    return output.some((item) => {
        if (!item || typeof item !== 'object') return false;
        if (item.type === 'message' || item.role === 'assistant' || item.role === 'user' || item.role === 'system') return true;
        if (!Array.isArray(item.content)) return false;
        return item.content.some((part) => {
            if (!part || typeof part !== 'object') return false;
            return typeof part.text === 'string' || part.type === 'output_text' || part.type === 'input_text';
        });
    });
}

function isConversationExcludedPath(path) {
    if (!path) return false;
    const p = String(path).toLowerCase();
    return p === '/v1/embeddings' ||
        p === '/v1/embeddings/' ||
        p.startsWith('/v1/embeddings?') ||
        p.startsWith('/v1/embeddings/');
}

function isConversationalPath(path) {
    if (!path) return false;
    return !!followUpEndpointKind(path);
}

function normalizedInteractionPath(path) {
    const raw = String(path || '').trim();
    const withoutQuery = raw.split('?')[0].replace(/\/+$/, '').toLowerCase();
    return withoutQuery.startsWith('/v1/') ? withoutQuery.slice(3) : withoutQuery;
}

// followUpEndpointKind intentionally gates the composer more narrowly than
// canShowConversation: passthrough endpoints may have conversation-shaped
// bodies, but GoModel cannot safely infer how to continue them.
export function followUpEndpointKind(path) {
    switch (normalizedInteractionPath(path)) {
    case '/chat/completions':
        return 'chat';
    case '/responses':
        return 'responses';
    case '/messages':
        return 'messages';
    default:
        return '';
    }
}

function cloneJSON(value) {
    if (!value || typeof value !== 'object') return null;
    try {
        return JSON.parse(JSON.stringify(value));
    } catch {
        return null;
    }
}

function appendIfMissing(messages, message) {
    if (!message || typeof message !== 'object') return;
    const previous = messages[messages.length - 1];
    try {
        if (previous && JSON.stringify(previous) === JSON.stringify(message)) return;
    } catch { /* append when comparison is unavailable */ }
    messages.push(message);
}

// buildFollowUpRequest reuses the latest request's model/options and extends
// it according to the endpoint's native conversation contract.
export function buildFollowUpRequest(entry, text) {
    const message = String(text || '').trim();
    const kind = followUpEndpointKind(entry && entry.path);
    const requestBody = cloneJSON(entry && entry.data && entry.data.request_body);
    if (!message || !kind || !requestBody) return null;

    const responseBody = entry && entry.data ? entry.data.response_body : null;
    if (kind === 'responses') {
        requestBody.input = message;
        const responseID = responseBody && typeof responseBody.id === 'string'
            ? responseBody.id.trim()
            : '';
        if (responseID && requestBody.conversation == null) {
            requestBody.previous_response_id = responseID;
        } else if (!responseID || requestBody.conversation != null) {
            delete requestBody.previous_response_id;
        }
        return requestBody;
    }

    const messages = Array.isArray(requestBody.messages) ? requestBody.messages : [];
    if (kind === 'chat') {
        const choice = responseBody && Array.isArray(responseBody.choices)
            ? responseBody.choices[0]
            : null;
        if (choice && choice.message) appendIfMissing(messages, cloneJSON(choice.message));
        messages.push({ role: 'user', content: message });
    } else {
        if (responseBody && responseBody.content !== undefined) {
            appendIfMissing(messages, {
                role: String(responseBody.role || 'assistant'),
                content: cloneJSON(responseBody.content) || responseBody.content,
            });
        }
        messages.push({ role: 'user', content: message });
    }
    requestBody.messages = messages;
    return requestBody;
}

const blockedFollowUpHeaders = new Set([
    'authorization', 'proxy-authorization', 'cookie', 'content-length', 'host',
    'connection', 'accept-encoding', 'content-encoding', 'transfer-encoding',
    'user-agent', 'date', 'expect', 'upgrade', 'via', 'te', 'trailer',
    'keep-alive', 'origin', 'referer', 'forwarded', 'x-real-ip', 'traceparent',
    'tracestate', 'x-request-id', 'idempotency-key', 'x-idempotency-key',
    'x-gomodel-timezone', 'x-gomodel-interaction-parent'
]);

// Persisted headers are already credential-redacted server-side. This second
// gate prevents redaction placeholders, browser-owned transport headers, and
// old request/trace IDs from being replayed while preserving captured session,
// user-path, label, and other application headers.
export function buildFollowUpHeaders(entry, anchorID) {
    const original = entry && entry.data && entry.data.request_headers;
    const headers = {};
    if (original && typeof original === 'object') {
        Object.keys(original).forEach((name) => {
            const lower = String(name).toLowerCase();
            const value = String(original[name] == null ? '' : original[name]);
            if (!name || !value || value === '[REDACTED]') return;
            if (blockedFollowUpHeaders.has(lower) || lower.startsWith('sec-') ||
                lower.startsWith('cf-') || lower.startsWith('x-forwarded-')) return;
            headers[name] = value;
        });
    }
    // The server inherits the resolved session from this parent. Replaying a
    // derived auto-/scoped session as a raw client header would scope it again.
    headers['X-GoModel-Interaction-Parent'] = String(anchorID || entry && entry.id || '').trim();
    return headers;
}

export function interactionParentID(entry) {
    const headers = entry && entry.data && entry.data.request_headers;
    if (!headers || typeof headers !== 'object') return '';
    const key = Object.keys(headers).find((name) => name.toLowerCase() === 'x-gomodel-interaction-parent');
    return key ? String(headers[key] || '').trim() : '';
}

// Follow-ups branch from the record the operator opened, even when newer
// records exist in the same session.
export function conversationFollowUpEntry(entries, anchorID) {
    if (!Array.isArray(entries) || !anchorID) return null;
    return entries.find((entry) => String(entry && entry.id || '') === String(anchorID)) || null;
}

export function latestConversationEntry(entries) {
    if (!Array.isArray(entries) || entries.length === 0) return null;
    return entries.reduce((latest, entry) => {
        if (!latest) return entry;
        const latestTime = new Date(latest.timestamp).getTime();
        const entryTime = new Date(entry && entry.timestamp).getTime();
        if (!Number.isFinite(latestTime)) return entry;
        if (!Number.isFinite(entryTime)) return latest;
        return entryTime >= latestTime ? entry : latest;
    }, null);
}

export function latestRenderableConversationEntry(entries, entryIDs) {
    const accepted = new Set(Array.isArray(entryIDs) ? entryIDs.map(String) : []);
    return latestConversationEntry((Array.isArray(entries) ? entries : []).filter((entry) => {
        const id = String(entry && entry.id || '');
        return accepted.has(id) && entry && entry.data && entry.data.request_body != null;
    }));
}

export function conversationEntryIsLatest(entries, entryID) {
    const latest = latestConversationEntry(entries);
    return !!latest && String(latest.id || '') === String(entryID || '');
}

function hasConversationPayload(entry) {
    // Slim list entries carry the server-computed signal instead of the
    // bodies it was derived from.
    if (entry && entry.conversation_payload) return true;

    const requestBody = entry && entry.data ? entry.data.request_body : null;
    const responseBody = entry && entry.data ? entry.data.response_body : null;

    const reqHas = requestBody && (
        Array.isArray(requestBody.messages) ||
        requestBody.input !== undefined ||
        typeof requestBody.instructions === 'string' ||
        typeof requestBody.previous_response_id === 'string'
    );
    const respHas = responseBody && (
        Array.isArray(responseBody.choices) ||
        looksLikeResponsesOutput(responseBody.output)
    );

    return !!(reqHas || respHas);
}

export function canShowConversation(entry) {
    if (!entry) return false;
    if (isConversationExcludedPath(entry.path)) return false;
    return isConversationalPath(entry.path) || hasConversationPayload(entry);
}

function jsonBracketDelta(text) {
    let depth = 0;
    let inString = false;
    let escaped = false;
    const src = String(text || '');

    for (let i = 0; i < src.length; i++) {
        const ch = src[i];
        if (inString) {
            if (escaped) {
                escaped = false;
                continue;
            }
            if (ch === '\\') {
                escaped = true;
                continue;
            }
            if (ch === '"') {
                inString = false;
            }
            continue;
        }

        if (ch === '"') {
            inString = true;
            continue;
        }
        if (ch === '{' || ch === '[') {
            depth++;
            continue;
        }
        if (ch === '}' || ch === ']') {
            depth--;
        }
    }

    return depth;
}

function findConversationSectionEnd(lines, startIdx, valuePart) {
    const value = String(valuePart || '').trim();
    if (!(value.startsWith('{') || value.startsWith('['))) {
        return startIdx;
    }

    let depth = jsonBracketDelta(valuePart);
    let idx = startIdx;
    while (depth > 0 && idx + 1 < lines.length) {
        idx++;
        depth += jsonBracketDelta(lines[idx]);
    }
    return idx;
}

function conversationHighlightRoleClass(key) {
    if (key === 'instructions') return 'conversation-system';
    if (key === 'messages' || key === 'input' || key === 'previous_response_id') return 'conversation-user';
    return 'conversation-assistant';
}

function escapeHTML(value) {
    return String(value == null ? '' : value)
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
}

// isAudioBody detects the audit value produced for audio endpoint bodies
// (see auditlog.AudioBodyLog): an object carrying the "__audio__" marker.
export function isAudioBody(value) {
    return !!(value && typeof value === 'object' && value.__audio__ === true);
}

function formatByteSize(bytes) {
    const n = Number(bytes || 0);
    if (!Number.isFinite(n) || n <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    let i = 0;
    let size = n;
    while (size >= 1024 && i < units.length - 1) {
        size /= 1024;
        i++;
    }
    return (i === 0 ? String(size) : size.toFixed(1)) + ' ' + units[i];
}

function sanitizeAudioContentType(value) {
    const ct = String(value || '').trim();
    return /^audio\/[a-zA-Z0-9.+-]+$/.test(ct) ? ct : 'audio/mpeg';
}

function formatAudioMetaValue(value) {
    if (value == null) return '';
    if (typeof value === 'object') {
        try {
            return JSON.stringify(value);
        } catch {
            return String(value);
        }
    }
    return String(value);
}

// renderAudioMeta renders the optional request-parameter metadata attached to
// an audio body (e.g. a transcription upload's model and options).
function renderAudioMeta(meta) {
    if (!meta || typeof meta !== 'object') return '';
    const rows = Object.keys(meta).map((key) =>
        '<div class="audit-audio-meta-row">'
        + '<span class="audit-audio-meta-key mono">' + escapeHTML(key) + '</span>'
        + '<span class="mono">' + escapeHTML(formatAudioMetaValue(meta[key])) + '</span>'
        + '</div>');
    if (!rows.length) return '';
    return '<div class="audit-audio-metadata">' + rows.join('') + '</div>';
}

// renderAudioBody renders an audio body as a player when the audio bytes
// were captured (base64), otherwise a labeled placeholder explaining why.
// Any attached request metadata is listed below.
export function renderAudioBody(value) {
    const contentType = sanitizeAudioContentType(value.content_type);
    const metaLabel = escapeHTML(contentType + ' · ' + formatByteSize(value.bytes));
    const metaBlock = renderAudioMeta(value.meta);
    if (value.stored && value.encoding === 'base64' && value.data) {
        const b64 = String(value.data).replace(/[^A-Za-z0-9+/=]/g, '');
        const src = 'data:' + contentType + ';base64,' + b64;
        return '<div class="audit-audio">'
            + '<audio class="audit-audio-player" controls preload="none" src="' + src + '"></audio>'
            + '<div class="audit-audio-meta mono">' + metaLabel + '</div>'
            + metaBlock
            + '</div>';
    }
    const reason = value.too_large
        ? 'Audio too large to store.'
        : 'Audio not logged. Set LOGGING_LOG_AUDIO_BODIES=true to capture playable audio.';
    return '<div class="audit-audio audit-audio-empty">'
        + '<div class="audit-audio-icon" aria-hidden="true">🔊</div>'
        + '<div class="audit-audio-meta mono">' + metaLabel + '</div>'
        + '<div class="audit-audio-note">' + escapeHTML(reason) + '</div>'
        + metaBlock
        + '</div>';
}

function jsonStringContent(value) {
    try {
        return JSON.stringify(String(value)).slice(1, -1);
    } catch {
        return '';
    }
}

function createPromptCacheHighlightState(highlight) {
    if (!highlight || typeof highlight !== 'object') return null;
    const characters = Number(highlight.characters || 0);
    if (!Number.isFinite(characters) || characters <= 0) return null;
    const segments = Array.isArray(highlight.segments)
        ? highlight.segments.map((segment) => String(segment || '')).filter(Boolean)
        : [];
    if (segments.length === 0) return null;
    return {
        remaining: Math.floor(characters),
        segments,
        segmentIndex: 0
    };
}

function renderLineWithPromptCacheHighlight(line, state) {
    if (!state || state.remaining <= 0 || state.segmentIndex >= state.segments.length) {
        return escapeHTML(line);
    }

    let rendered = '';
    let cursor = 0;
    let searchFrom = 0;

    while (state.remaining > 0 && state.segmentIndex < state.segments.length) {
        const segment = state.segments[state.segmentIndex];
        const encodedSegment = jsonStringContent(segment);
        if (!encodedSegment) {
            state.segmentIndex++;
            continue;
        }

        const idx = line.indexOf(encodedSegment, searchFrom);
        if (idx < 0) {
            break;
        }

        const highlightedChars = Math.min(state.remaining, segment.length);
        const encodedHighlight = jsonStringContent(segment.slice(0, highlightedChars));
        if (!encodedHighlight) {
            state.segmentIndex++;
            continue;
        }

        rendered += escapeHTML(line.slice(cursor, idx));
        rendered += '<span class="audit-prompt-cache-highlight">' + escapeHTML(encodedHighlight) + '</span>';

        cursor = idx + encodedHighlight.length;
        searchFrom = idx + encodedSegment.length;
        state.remaining -= highlightedChars;

        if (highlightedChars >= segment.length) {
            state.segmentIndex++;
            continue;
        }
        break;
    }

    if (!rendered) {
        return escapeHTML(line);
    }
    return rendered + escapeHTML(line.slice(cursor));
}

export function renderBodyWithConversationHighlights(entry, value, deps) {
    const formatJSONFn = deps && typeof deps.formatJSON === 'function' ? deps.formatJSON : (v) => String(v);
    const canShow = deps && typeof deps.canShowConversation === 'function' ? deps.canShowConversation : () => false;
    const promptCacheState = createPromptCacheHighlightState(deps && deps.promptCacheHighlight);

    const raw = formatJSONFn(value);
    if (!raw || raw === 'Not captured') {
        return escapeHTML(raw);
    }

    const showConversation = canShow(entry);
    if (!showConversation) {
        return raw.split('\n').map((line) => renderLineWithPromptCacheHighlight(line, promptCacheState)).join('\n');
    }

    const lines = raw.split('\n');
    const rendered = [];

    let i = 0;
    while (i < lines.length) {
        const line = lines[i];
        const match = line.match(/^(\s*)"([^"]+)"\s*:\s*(.*)$/);
        if (match && sectionKeys.has(match[2])) {
            const key = match[2];
            const valuePart = match[3] || '';
            const end = findConversationSectionEnd(lines, i, valuePart);
            const roleClass = conversationHighlightRoleClass(key);
            const block = lines.slice(i, end + 1).map((l) => renderLineWithPromptCacheHighlight(l, promptCacheState)).join('\n');
            rendered.push('<span class="conversation-body-highlight ' + roleClass + '" data-conversation-trigger="1">' + block + '</span>');
            i = end + 1;
            continue;
        }
        rendered.push(renderLineWithPromptCacheHighlight(line, promptCacheState));
        i++;
    }

    return rendered.join('\n');
}

function roleMeta(role) {
    const normalized = String(role || '').toLowerCase();
    if (normalized === 'system' || normalized === 'developer') {
        return { role: 'system', label: 'System Prompt', className: 'role-system' };
    }
    if (normalized === 'assistant') {
        return { role: 'assistant', label: 'Agent', className: 'role-assistant' };
    }
    if (normalized === 'error') {
        return { role: 'error', label: 'Error', className: 'role-error' };
    }
    if (normalized === 'function_call') {
        return { role: 'function_call', label: 'Function Call', className: 'role-function-call' };
    }
    if (normalized === 'function_result') {
        return { role: 'function_result', label: 'Function Result', className: 'role-function-result' };
    }
    return { role: 'user', label: 'User', className: 'role-user' };
}

function conversationMessage(role, text, timestamp, entryID, isAnchor, isAfterAnchor, idx, toolCalls, functionName) {
    const normalized = roleMeta(role);
    return {
        uid: entryID + '-' + idx,
        entryID,
        timestamp,
        text,
        role: normalized.role,
        roleLabel: normalized.label,
        roleClass: normalized.className,
        isAnchor,
        isAfterAnchor,
        toolCalls: Array.isArray(toolCalls) && toolCalls.length > 0 ? toolCalls : null,
        functionName: functionName || ''
    };
}

function extractToolCallsList(toolCalls) {
    if (!Array.isArray(toolCalls)) return [];
    return toolCalls.map((tc) => {
        if (!tc) return null;
        const fn = tc.function || tc;
        return {
            name: fn.name || tc.name || '',
            arguments: fn.arguments || tc.arguments || ''
        };
    }).filter(Boolean);
}

function collectCallIds(map, requestBody, responseBody) {
    if (requestBody && Array.isArray(requestBody.messages)) {
        requestBody.messages.forEach((m) => {
            if (!m || !Array.isArray(m.tool_calls)) return;
            m.tool_calls.forEach((tc) => {
                if (!tc) return;
                const id = tc.id || '';
                const fn = tc.function || tc;
                const name = fn.name || tc.name || '';
                if (id && name) map[id] = name;
            });
        });
    }
    if (requestBody && Array.isArray(requestBody.input)) {
        requestBody.input.forEach((item) => {
            if (!item || typeof item !== 'object' || item.type !== 'function_call') return;
            const id = item.id || item.call_id || '';
            const name = item.name || '';
            if (id && name) map[id] = name;
        });
    }
    if (responseBody && Array.isArray(responseBody.choices)) {
        const first = responseBody.choices[0];
        if (first && first.message && Array.isArray(first.message.tool_calls)) {
            first.message.tool_calls.forEach((tc) => {
                if (!tc) return;
                const id = tc.id || '';
                const fn = tc.function || tc;
                const name = fn.name || tc.name || '';
                if (id && name) map[id] = name;
            });
        }
    }
    if (responseBody && Array.isArray(responseBody.output)) {
        responseBody.output.forEach((item) => {
            if (!item || item.type !== 'function_call') return;
            const id = item.id || item.call_id || '';
            const name = item.name || '';
            if (id && name) map[id] = name;
        });
    }
}

function stableJSON(value) {
    if (Array.isArray(value)) return '[' + value.map(stableJSON).join(',') + ']';
    if (value && typeof value === 'object') {
        return '{' + Object.keys(value).sort().map((key) =>
            JSON.stringify(key) + ':' + stableJSON(value[key])).join(',') + '}';
    }
    return JSON.stringify(value);
}

function fullSnapshotLineage(requestBody) {
    if (!requestBody || typeof requestBody !== 'object') return null;
    const values = [];
    if (requestBody.system !== undefined) values.push({ system: requestBody.system });
    if (requestBody.instructions !== undefined) values.push({ instructions: requestBody.instructions });
    if (Array.isArray(requestBody.messages)) values.push(...requestBody.messages);
    else if (Array.isArray(requestBody.input) &&
        requestBody.previous_response_id == null && requestBody.conversation == null) {
        values.push(...requestBody.input);
    } else {
        return null;
    }
    return values.map(stableJSON);
}

function conversationReference(requestBody) {
    const value = requestBody && requestBody.conversation;
    if (typeof value === 'string') return value.trim();
    if (value && typeof value.id === 'string') return value.id.trim();
    return '';
}

function compareConversationEntries(a, b) {
    const aTime = new Date(a && a.timestamp).getTime();
    const bTime = new Date(b && b.timestamp).getTime();
    if (Number.isFinite(aTime) && Number.isFinite(bTime) && aTime !== bTime) return aTime - bTime;
    return String(a && a.timestamp || '').localeCompare(String(b && b.timestamp || ''));
}

export function buildConversationView(entries, anchorID) {
    if (!Array.isArray(entries) || entries.length === 0) return { messages: [], entryIDs: [] };

    const sorted = [...entries].sort(compareConversationEntries);

    const callIdMap = {};
    sorted.forEach((entry) => {
        const rb = entry.data && entry.data.request_body ? entry.data.request_body : null;
        const rsb = entry.data && entry.data.response_body ? entry.data.response_body : null;
        collectCallIds(callIdMap, rb, rsb);
    });

    const messages = [];
    const branchEntryIDSet = new Set();
    const acceptedResponseIDs = new Set();
    const acceptedConversationIDs = new Set();
    let historySignatures = [];
    let anchorLineage = null;
    let idx = 0;
    const anchorIndex = sorted.findIndex((entry) => entry.id === anchorID);

    const signature = (message) => JSON.stringify({
        role: message.role,
        text: message.text,
        toolCalls: message.toolCalls,
        functionName: message.functionName,
    });

    sorted.forEach((entry, entryIndex) => {
        const isAnchor = entry.id === anchorID;
        const isAfterAnchor = anchorIndex >= 0 && entryIndex > anchorIndex;
        const ts = entry.timestamp;
        const requestBody = entry.data && entry.data.request_body ? entry.data.request_body : null;
        const responseBody = entry.data && entry.data.response_body ? entry.data.response_body : null;
        const requestStart = messages.length;

        if (requestBody && typeof requestBody.instructions === 'string' && requestBody.instructions.trim()) {
            messages.push(conversationMessage('system', requestBody.instructions, ts, entry.id, isAnchor, isAfterAnchor, ++idx));
        }

        if (requestBody && Array.isArray(requestBody.messages)) {
            requestBody.messages.forEach((m) => {
                if (!m) return;
                const role = (m.role || 'user').toLowerCase();
                if (role === 'tool') {
                    const text = extractText(m.content);
                    const fnName = m.name || callIdMap[m.tool_call_id] || '';
                    if (text) messages.push(conversationMessage('function_result', text, ts, entry.id, isAnchor, isAfterAnchor, ++idx, [], fnName));
                    return;
                }
                if (role === 'assistant') {
                    const text = extractText(m.content);
                    const toolCalls = extractToolCallsList(m.tool_calls);
                    if (text || toolCalls.length > 0) {
                        messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, isAfterAnchor, ++idx, toolCalls));
                    }
                    return;
                }
                const text = extractText(m.content);
                if (text) messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, isAfterAnchor, ++idx));
            });
        }

        if (requestBody && requestBody.input !== undefined) {
            if (Array.isArray(requestBody.input)) {
                requestBody.input.forEach((item) => {
                    if (!item || typeof item !== 'object') return;
                    if (item.type === 'function_call_output') {
                        const text = typeof item.output === 'string' ? item.output : extractText(item.output);
                        if (text) messages.push(conversationMessage('function_result', text, ts, entry.id, isAnchor, isAfterAnchor, ++idx, [], callIdMap[item.call_id] || ''));
                    } else if (item.type === 'function_call') {
                        messages.push(conversationMessage('function_call', '', ts, entry.id, isAnchor, isAfterAnchor, ++idx, [{ name: item.name || '', arguments: item.arguments || '' }]));
                    } else if (item.role) {
                        const role = String(item.role).toLowerCase();
                        const text = extractText(item.content);
                        if (text) messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, isAfterAnchor, ++idx));
                    }
                });
            } else {
                extractResponsesInputMessages(requestBody.input).forEach((m) => {
                    if (m.text) messages.push(conversationMessage(m.role, m.text, ts, entry.id, isAnchor, isAfterAnchor, ++idx));
                });
            }
        }

        // Chat-completions and Messages requests are complete conversation
        // snapshots. Treat the newest snapshot as authoritative and preserve
        // provenance only for its unchanged prefix. This guarantees that a
        // changed or normalized historical message cannot make us append the
        // whole history a second time.
        //
        // Responses requests using previous_response_id/conversation may carry
        // only a delta, so those continue to extend the rendered transcript.
        const requestMessages = messages.splice(requestStart);
        const requestSignatures = requestMessages.map(signature);
        const lineage = fullSnapshotLineage(requestBody);
        const isFullSnapshot = lineage !== null;
        const parentID = interactionParentID(entry);
        const linkedParent = !!parentID && branchEntryIDSet.has(parentID);
        const previousResponseID = String(requestBody && requestBody.previous_response_id || '').trim();
        const conversationID = conversationReference(requestBody);
        const linkedDelta = linkedParent ||
            (!!previousResponseID && acceptedResponseIDs.has(previousResponseID)) ||
            (!!conversationID && acceptedConversationIDs.has(conversationID));

        if (isAfterAnchor) {
            const retainsAnchor = isFullSnapshot && anchorLineage && anchorLineage.length > 0 &&
                anchorLineage.every((value, i) => lineage[i] === value);
            if ((isFullSnapshot && !retainsAnchor && !linkedParent) ||
                (!isFullSnapshot && !linkedDelta)) return;
        }
        if (isAnchor && isFullSnapshot) {
            anchorLineage = [...lineage];
        }

        const entryID = String(entry.id || '').trim();
        if (isAnchor) {
            branchEntryIDSet.clear();
            acceptedResponseIDs.clear();
            acceptedConversationIDs.clear();
        }
        if (entryID) {
            if (isAnchor || isAfterAnchor) branchEntryIDSet.add(entryID);
        }
        if (conversationID) acceptedConversationIDs.add(conversationID);
        let commonPrefix = 0;
        while (commonPrefix < requestSignatures.length &&
            commonPrefix < historySignatures.length &&
            requestSignatures[commonPrefix] === historySignatures[commonPrefix]) {
            commonPrefix++;
        }
        if (isFullSnapshot) {
            for (let i = 0; i < commonPrefix; i++) {
                requestMessages[i] = {
                    ...requestMessages[i],
                    uid: messages[i].uid,
                    entryID: messages[i].entryID,
                    timestamp: messages[i].timestamp,
                    isAnchor: messages[i].isAnchor,
                    isAfterAnchor: messages[i].isAfterAnchor,
                };
            }
            messages.splice(0, messages.length, ...requestMessages);
            historySignatures = requestSignatures;
        } else {
            let overlap = Math.min(historySignatures.length, requestSignatures.length);
            while (overlap > 0) {
                const historySuffix = historySignatures.slice(-overlap);
                const requestPrefix = requestSignatures.slice(0, overlap);
                if (historySuffix.every((value, i) => value === requestPrefix[i])) break;
                overlap--;
            }
            messages.push(...requestMessages.slice(overlap));
            historySignatures.push(...requestSignatures.slice(overlap));
        }

        if (responseBody && Array.isArray(responseBody.choices)) {
            const first = responseBody.choices[0];
            if (first && first.message) {
                const role = (first.message.role || 'assistant').toLowerCase();
                const text = extractText(first.message.content);
                const toolCalls = extractToolCallsList(first.message.tool_calls);
                if (text || toolCalls.length > 0) {
                    const responseMessage = conversationMessage(role, text, ts, entry.id, isAnchor, isAfterAnchor, ++idx, toolCalls);
                    messages.push(responseMessage);
                    historySignatures.push(signature(responseMessage));
                }
            }
        }

        if (responseBody && Array.isArray(responseBody.output)) {
            responseBody.output.forEach((item) => {
                if (!item) return;
                if (item.type === 'function_call') {
                    const responseMessage = conversationMessage('function_call', '', ts, entry.id, isAnchor, isAfterAnchor, ++idx, [{ name: item.name || '', arguments: item.arguments || '' }]);
                    messages.push(responseMessage);
                    historySignatures.push(signature(responseMessage));
                    return;
                }
                const role = (item.role || 'assistant').toLowerCase();
                const text = extractResponsesOutputText(item);
                if (text) {
                    const responseMessage = conversationMessage(role, text, ts, entry.id, isAnchor, isAfterAnchor, ++idx);
                    messages.push(responseMessage);
                    historySignatures.push(signature(responseMessage));
                }
            });
        }

        // Anthropic-compatible /messages responses carry assistant content at
        // the top level instead of under choices/output.
        if (responseBody && responseBody.content !== undefined &&
            !Array.isArray(responseBody.choices) && !Array.isArray(responseBody.output)) {
            const role = String(responseBody.role || 'assistant').toLowerCase();
            const text = extractText(responseBody.content);
            if (text) {
                const responseMessage = conversationMessage(role, text, ts, entry.id, isAnchor, isAfterAnchor, ++idx);
                messages.push(responseMessage);
                historySignatures.push(signature(responseMessage));
            }
        }

        const errMsg = extractConversationErrorMessage(entry);
        if (errMsg) {
            messages.push(conversationMessage('error', errMsg, ts, entry.id, isAnchor, isAfterAnchor, ++idx));
        }

        const responseID = String(responseBody && responseBody.id || '').trim();
        if (responseID) acceptedResponseIDs.add(responseID);
    });

    return { messages, entryIDs: [...branchEntryIDSet] };
}

export function buildConversationMessages(entries, anchorID) {
    return buildConversationView(entries, anchorID).messages;
}

export function functionExpandedContent(msg) {
    if (msg.role === 'function_call') {
        return (msg.toolCalls || []).map(function (tc) {
            let args = tc.arguments || '';
            try { args = JSON.stringify(JSON.parse(args), null, 2); } catch { /* keep raw */ }
            return tc.name + '(' + args + ')';
        }).join('\n\n');
    }
    return msg.text || '';
}
