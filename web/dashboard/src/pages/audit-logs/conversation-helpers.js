// Pure interaction shaping shared by the Svelte stores and Node tests.

import { formatJSON } from "../../lib/utils/format.js";
import { findNestedErrorMessage, tryParseJSON } from "./error-text.js";

// Re-exported so the drawer store keeps one import for its formatting.
export { formatJSON };

const sectionKeys = new Set(['instructions', 'messages', 'input', 'previous_response_id', 'choices', 'output']);

function contentPartLabel(part) {
    if (!part || typeof part !== 'object') return '';
    const type = String(part.type || '').toLowerCase();
    if (type === 'image' || type === 'image_url' || type === 'input_image' || type === 'output_image') {
        return '[Image]';
    }
    if (type === 'audio' || type === 'input_audio' || type === 'output_audio') {
        return '[Audio]';
    }
    if (type === 'file' || type === 'input_file' || type === 'output_file') {
        const name = String(part.filename || part.name || '').trim();
        return name ? '[File: ' + name + ']' : '[File]';
    }
    if (typeof part.refusal === 'string') return part.refusal;
    return '';
}

function extractText(content) {
    if (content == null) return '';
    if (typeof content === 'string') return content.trim();

    if (Array.isArray(content)) {
        const parts = content.map((part) => {
            if (typeof part === 'string') return part;
            if (!part || typeof part !== 'object') return '';
            if (typeof part.text === 'string') return part.text;
            if (typeof part.output_text === 'string') return part.output_text;
            return contentPartLabel(part);
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

function extractMessageText(message) {
    if (!message || typeof message !== 'object') return '';
    const text = extractText(message.content);
    if (text) return text;
    return typeof message.refusal === 'string' ? message.refusal.trim() : '';
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

function extractToolArgumentSegments(value) {
    if (value == null) return [];
    if (Array.isArray(value)) {
        return value.flatMap(extractToolArgumentSegments);
    }
    if (typeof value === 'object') {
        return Object.entries(value).flatMap(([key, item]) => [
            key,
            ...extractToolArgumentSegments(item),
        ]);
    }
    const text = String(value);
    return text ? [text] : [];
}

function extractToolCallPromptTextSegments(toolCalls) {
    return extractToolCallsList(toolCalls).flatMap((toolCall) => [
        ...(toolCall.name ? [String(toolCall.name)] : []),
        ...extractToolArgumentSegments(toolCall.arguments),
    ]);
}

function extractContentPromptTextSegments(content) {
    if (!Array.isArray(content)) return extractTextSegments(content);
    return content.flatMap((part) => {
        if (part && typeof part === 'object' && part.type === 'tool_use') {
            return extractToolCallPromptTextSegments([part]);
        }
        return extractTextSegments(part);
    });
}

function estimatedPromptCachedCharacters(entry) {
    const rawValue = entry && entry.usage
        ? entry.usage.estimated_cached_characters
        : null;
    if (rawValue == null) return null;
    const value = Number(rawValue);
    return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

function measurePromptCache(text, sourceSegments) {
    if (!text) return null;
    const sourceCharacters = (Array.isArray(sourceSegments) ? sourceSegments : [])
        .reduce((total, source) => total + String(source || '').length, 0);
    const total = Math.min(String(text).length, sourceCharacters);
    return total > 0 ? { total } : null;
}

function toolCallPromptCharacters(toolCall) {
    const value = toolCall && toolCall.arguments !== undefined ? toolCall.arguments : '';
    let argumentsText = '';
    if (typeof value === 'string') {
        argumentsText = value;
    } else {
        try {
            argumentsText = JSON.stringify(value);
        } catch {
            argumentsText = String(value);
        }
    }
    return (String(toolCall && toolCall.name || '') + '(' + argumentsText + ')').length;
}

function measureToolCallPromptCache(toolCalls) {
    const characters = (Array.isArray(toolCalls) ? toolCalls : []).map(toolCallPromptCharacters);
    return { characters };
}

function applyPromptCacheFill(messages, cachedCharacters) {
    let remaining = Math.max(0, Number(cachedCharacters || 0));
    return messages.map((message) => {
        if (!message || message.role === 'error') return message;

        const textCharacters = Math.max(0, Number(message._promptTextCharacters || 0));
        const cachedTextCharacters = Math.min(remaining, textCharacters);
        remaining -= cachedTextCharacters;

        let cachedToolCharacters = 0;
        (Array.isArray(message._toolPromptCharacters)
            ? message._toolPromptCharacters
            : []).forEach((value) => {
            const characters = Math.max(0, Number(value || 0));
            const cached = Math.min(remaining, characters);
            remaining -= cached;
            cachedToolCharacters += cached;
        });

        const toolCharacters = (message._toolPromptCharacters || [])
            .reduce((total, value) => total + Math.max(0, Number(value || 0)), 0);
        const totalCharacters = textCharacters + toolCharacters;
        return {
            ...message,
            promptCacheRatio: totalCharacters > 0
                ? (cachedTextCharacters + cachedToolCharacters) / totalCharacters
                : 0,
        };
    });
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
    return extractMessageText(item);
}

export function extractRequestPromptTextSegments(body) {
    if (!body || typeof body !== 'object') return [];

    const segments = [];
    segments.push(...extractTextSegments(body.instructions));

    if (Array.isArray(body.messages)) {
        body.messages.forEach((message) => {
            if (!message || typeof message !== 'object') return;
            segments.push(...extractContentPromptTextSegments(message.content));
            segments.push(...extractToolCallPromptTextSegments(message.tool_calls));
        });
    }

    if (typeof body.input === 'string') {
        segments.push(body.input);
    } else if (Array.isArray(body.input)) {
        body.input.forEach((item) => {
            if (!item || typeof item !== 'object') return;
            if (item.type === 'function_call' || item.type === 'tool_use') {
                segments.push(...extractToolCallPromptTextSegments([item]));
                return;
            }
            segments.push(...extractContentPromptTextSegments(item.content));
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
export function canBuildFollowUpRequest(entry) {
    const kind = followUpEndpointKind(entry && entry.path);
    const requestBody = entry && entry.data && entry.data.request_body;
    if (!kind || !requestBody || typeof requestBody !== 'object') return false;
    if (kind !== 'responses') return true;
    const responseID = entry.data && entry.data.response_body &&
        typeof entry.data.response_body.id === 'string'
        ? entry.data.response_body.id.trim()
        : '';
    return responseID !== '' || conversationReference(requestBody) !== '';
}

export function buildFollowUpRequest(entry, text) {
    const message = String(text || '').trim();
    const kind = followUpEndpointKind(entry && entry.path);
    const requestBody = cloneJSON(entry && entry.data && entry.data.request_body);
    if (!message || !kind || !requestBody) return null;

    const responseBody = entry && entry.data ? entry.data.response_body : null;
    if (kind === 'responses') {
        const responseID = responseBody && typeof responseBody.id === 'string'
            ? responseBody.id.trim()
            : '';
        const conversationID = conversationReference(requestBody);
        if (!responseID && !conversationID) return null;
        requestBody.input = message;
        if (responseID && !conversationID) {
            requestBody.previous_response_id = responseID;
        } else {
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
    'authorization', 'proxy-authorization', 'cookie', 'content-type', 'content-length', 'host',
    'accept',
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
export function buildFollowUpHeaders(entry, anchorID, requestID = '') {
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
    if (String(requestID || '').trim()) headers['X-Request-ID'] = String(requestID).trim();
    return headers;
}

export function conversationEntryByRequestID(entries, requestID) {
    const wanted = String(requestID || '').trim();
    if (!wanted || !Array.isArray(entries)) return null;
    return entries.find((entry) => String(entry && entry.request_id || '').trim() === wanted) || null;
}

export function matchLiveConversationEntry(entries, anchorID, sessionID, followUpRequestID, entry) {
    const currentEntries = Array.isArray(entries) ? entries : [];
    const entryID = String(entry && entry.id || '').trim();
    const entryRequestID = String(entry && entry.request_id || '').trim();
    const entrySessionID = String(entry && entry.session_id || '').trim();
    const correlationID = String(followUpRequestID || '').trim();
    const parentID = interactionParentID(entry);
    const knownEntry = currentEntries.some((candidate) =>
        (entryID && String(candidate && candidate.id || '').trim() === entryID) ||
        (entryRequestID && String(candidate && candidate.request_id || '').trim() === entryRequestID));
    const submittedChild = !!correlationID && entryRequestID === correlationID;
    const sameSession = !!sessionID && entrySessionID === String(sessionID).trim();
    const linkedParent = !!parentID && (parentID === String(anchorID || '').trim() ||
        currentEntries.some((candidate) => String(candidate && candidate.id || '').trim() === parentID));
    const accepted = knownEntry || submittedChild || (!correlationID && (sameSession || linkedParent));

    return {
        accepted,
        submittedChild,
        followUpRequestID: accepted && submittedChild && entryID ? '' : correlationID,
    };
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
        return compareConversationEntries(entry, latest) >= 0 ? entry : latest;
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

export function shouldHydrateConversation(eventType, changedEntryID, selectedAnchorID) {
    return String(eventType || '').trim() === 'audit.flushed' &&
        String(changedEntryID || '').trim() !== '' &&
        String(changedEntryID || '').trim() === String(selectedAnchorID || '').trim();
}

export function mergedConversationEntryIDs(existingIDs, entries) {
    const ids = Array.isArray(existingIDs) ? existingIDs.filter(Boolean).map(String) : [];
    (Array.isArray(entries) ? entries : []).forEach((entry) => {
        const id = String(entry && (entry.id || entry.request_id) || '').trim();
        if (id && !ids.includes(id)) ids.push(id);
    });
    return ids;
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

function conversationMessage(role, text, {
    timestamp,
    entryID,
    isAnchor,
    isAfterAnchor,
    index,
    toolCalls,
    functionName,
    functionCallID,
    promptCache,
    toolPromptCache,
}) {
    const normalized = roleMeta(role);
    const promptTextCharacters = promptCache && Number.isFinite(Number(promptCache.total))
        ? Math.max(0, Number(promptCache.total))
        : promptCache === null
            ? 0
            : String(text || '').length;
    const toolPromptCharacters = toolPromptCache && Array.isArray(toolPromptCache.characters)
        ? toolPromptCache.characters
        : measureToolCallPromptCache(toolCalls).characters;
    return {
        uid: entryID + '-' + index,
        entryID,
        timestamp,
        text,
        role: normalized.role,
        roleLabel: normalized.label,
        roleClass: normalized.className,
        isAnchor,
        isAfterAnchor,
        toolCalls: Array.isArray(toolCalls) && toolCalls.length > 0 ? toolCalls : null,
        functionName: functionName || '',
        functionCallID: functionCallID || '',
        promptCacheRatio: 0,
        _promptTextCharacters: promptTextCharacters,
        _toolPromptCharacters: toolPromptCharacters,
    };
}

function extractToolCallsList(toolCalls) {
    if (!Array.isArray(toolCalls)) return [];
    return toolCalls.map((tc) => {
        if (!tc) return null;
        const fn = tc.function || tc;
        let args = '';
        if (fn.arguments !== undefined) args = fn.arguments;
        else if (tc.arguments !== undefined) args = tc.arguments;
        else if (fn.input !== undefined) args = fn.input;
        else if (tc.input !== undefined) args = tc.input;
        return {
            name: fn.name || tc.name || '',
            arguments: args,
            id: tc.call_id || tc.id || ''
        };
    }).filter(Boolean);
}

function extractContentToolCalls(content) {
    if (!Array.isArray(content)) return [];
    return extractToolCallsList(content.filter((part) =>
        part && typeof part === 'object' && part.type === 'tool_use'));
}

function extractContentToolResults(content) {
    if (!Array.isArray(content)) return [];
    return content.filter((part) =>
        part && typeof part === 'object' && part.type === 'tool_result').map((part) => ({
        id: part.tool_use_id || part.call_id || '',
        text: extractText(part.content),
    }));
}

function registerCallIds(map, item, name) {
    if (!item || !name) return;
    [item.call_id, item.id].forEach((id) => {
        if (id) map[id] = name;
    });
}

function collectCallIds(map, requestBody, responseBody) {
    if (requestBody && Array.isArray(requestBody.messages)) {
        requestBody.messages.forEach((m) => {
            if (!m) return;
            const toolCalls = [
                ...(Array.isArray(m.tool_calls) ? m.tool_calls : []),
                ...(Array.isArray(m.content) ? m.content.filter((part) => part && part.type === 'tool_use') : []),
            ];
            toolCalls.forEach((tc) => {
                if (!tc) return;
                const fn = tc.function || tc;
                const name = fn.name || tc.name || '';
                registerCallIds(map, tc, name);
            });
        });
    }
    if (requestBody && Array.isArray(requestBody.input)) {
        requestBody.input.forEach((item) => {
            if (!item || typeof item !== 'object' ||
                (item.type !== 'function_call' && item.type !== 'tool_use')) return;
            const name = item.name || '';
            registerCallIds(map, item, name);
        });
    }
    if (responseBody && Array.isArray(responseBody.choices)) {
        const first = responseBody.choices[0];
        if (first && first.message && Array.isArray(first.message.tool_calls)) {
            first.message.tool_calls.forEach((tc) => {
                if (!tc) return;
                const fn = tc.function || tc;
                const name = fn.name || tc.name || '';
                registerCallIds(map, tc, name);
            });
        }
    }
    if (responseBody && Array.isArray(responseBody.output)) {
        responseBody.output.forEach((item) => {
            if (!item || (item.type !== 'function_call' && item.type !== 'tool_use')) return;
            const name = item.name || '';
            registerCallIds(map, item, name);
        });
    }
    if (responseBody && Array.isArray(responseBody.content)) {
        responseBody.content.forEach((item) => {
            if (!item || item.type !== 'tool_use') return;
            const name = item.name || '';
            registerCallIds(map, item, name);
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
    const timestampOrder = String(a && a.timestamp || '').localeCompare(String(b && b.timestamp || ''));
    if (timestampOrder !== 0) return timestampOrder;
    const aID = String(a && a.id || '');
    const bID = String(b && b.id || '');
    return aID < bID ? -1 : aID > bID ? 1 : 0;
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
        functionCallID: message.functionCallID,
    });

    sorted.forEach((entry, entryIndex) => {
        const isAnchor = entry.id === anchorID;
        const isAfterAnchor = anchorIndex >= 0 && entryIndex > anchorIndex;
        const ts = entry.timestamp;
        const requestBody = entry.data && entry.data.request_body ? entry.data.request_body : null;
        const responseBody = entry.data && entry.data.response_body ? entry.data.response_body : null;
        const requestStart = messages.length;
        const cachedPrompt = (text, sourceSegments) =>
            measurePromptCache(text, sourceSegments);
        const cachedToolCalls = (toolCalls) =>
            measureToolCallPromptCache(toolCalls);
        const message = (role, text, {
            toolCalls,
            functionName,
            functionCallID,
            promptCache,
            toolPromptCache,
        } = {}) => conversationMessage(role, text, {
            timestamp: ts,
            entryID: entry.id,
            isAnchor,
            isAfterAnchor,
            index: ++idx,
            toolCalls,
            functionName,
            functionCallID,
            promptCache,
            toolPromptCache,
        });

        if (requestBody && requestBody.system !== undefined) {
            const text = extractText(requestBody.system);
            if (text) messages.push(message('system', text, {
                promptCache: cachedPrompt(text, extractTextSegments(requestBody.system)),
            }));
        }
        if (requestBody && requestBody.instructions !== undefined) {
            const text = extractText(requestBody.instructions);
            if (text) messages.push(message('system', text, {
                promptCache: cachedPrompt(text, extractTextSegments(requestBody.instructions)),
            }));
        }

        if (requestBody && Array.isArray(requestBody.messages)) {
            requestBody.messages.forEach((m) => {
                if (!m) return;
                const role = (m.role || 'user').toLowerCase();
                if (role === 'tool') {
                    const text = extractText(m.content);
                    const fnName = m.name || callIdMap[m.tool_call_id] || '';
                    if (text) messages.push(message('function_result', text, {
                        functionName: fnName,
                        functionCallID: m.tool_call_id || '',
                        promptCache: cachedPrompt(text, extractTextSegments(m.content)),
                    }));
                    return;
                }
                if (role === 'assistant') {
                    const text = extractMessageText(m);
                    const toolCalls = [
                        ...extractToolCallsList(m.tool_calls),
                        ...extractContentToolCalls(m.content),
                    ];
                    if (text || toolCalls.length > 0) {
                        const promptCache = cachedPrompt(text, extractTextSegments(m.content));
                        messages.push(message(role, text, {
                            toolCalls,
                            promptCache,
                            toolPromptCache: cachedToolCalls(toolCalls),
                        }));
                    }
                    return;
                }
                extractContentToolResults(m.content).forEach((result) => {
                    if (!result.text) return;
                    messages.push(message('function_result', result.text, {
                        functionName: callIdMap[result.id] || '',
                        functionCallID: result.id,
                        promptCache: cachedPrompt(result.text, [result.text]),
                    }));
                });
                const text = extractMessageText(m);
                if (text) messages.push(message(role, text, {
                    promptCache: cachedPrompt(text, extractTextSegments(m.content)),
                }));
            });
        }

        if (requestBody && requestBody.input !== undefined) {
            if (Array.isArray(requestBody.input)) {
                requestBody.input.forEach((item) => {
                    if (!item || typeof item !== 'object') return;
                    if (item.type === 'function_call_output' || item.type === 'tool_result') {
                        const isToolResult = item.type === 'tool_result';
                        const output = isToolResult ? item.content : item.output;
                        const callID = isToolResult
                            ? item.tool_use_id || item.call_id || ''
                            : item.call_id || '';
                        const text = typeof output === 'string' ? output : extractText(output);
                        if (text) messages.push(message('function_result', text, {
                            functionName: callIdMap[callID] || item.name || '',
                            functionCallID: callID,
                            promptCache: cachedPrompt(
                                text,
                                isToolResult ? extractTextSegments(item.content) : [text],
                            ),
                        }));
                    } else if (item.type === 'function_call' || item.type === 'tool_use') {
                        const toolCalls = extractToolCallsList([item]);
                        messages.push(message('function_call', '', {
                            toolCalls,
                            toolPromptCache: cachedToolCalls(toolCalls),
                        }));
                    } else if (item.role) {
                        const role = String(item.role).toLowerCase();
                        const text = extractText(item.content);
                        if (text) messages.push(message(role, text, {
                            promptCache: cachedPrompt(text, extractTextSegments(item.content)),
                        }));
                    }
                });
            } else {
                extractResponsesInputMessages(requestBody.input).forEach((m) => {
                    if (!m.text) return;
                    let sourceSegments = [];
                    if (typeof requestBody.input === 'string') sourceSegments = [requestBody.input];
                    else if (requestBody.input && typeof requestBody.input === 'object') {
                        sourceSegments = extractTextSegments(requestBody.input.content);
                        if (typeof requestBody.input.text === 'string') sourceSegments.push(requestBody.input.text);
                    }
                    messages.push(message(m.role, m.text, {
                        promptCache: cachedPrompt(m.text, sourceSegments),
                    }));
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

        // Provider cache reads cover a prefix of the complete input context.
        // For Responses chains that context includes earlier turns retained by
        // previous_response_id, even though they are absent from this request
        // body. Repaint the assembled prompt in display order so the fill can
        // never skip history and resume on a newer message.
        const cachedCharacters = estimatedPromptCachedCharacters(entry);
        if (requestBody && cachedCharacters !== null) {
            messages.splice(0, messages.length, ...applyPromptCacheFill(
                messages,
                cachedCharacters,
            ));
        }

        if (responseBody && Array.isArray(responseBody.choices)) {
            const first = responseBody.choices[0];
            if (first && first.message) {
                const role = (first.message.role || 'assistant').toLowerCase();
                const text = extractMessageText(first.message);
                const toolCalls = extractToolCallsList(first.message.tool_calls);
                if (text || toolCalls.length > 0) {
                    const responseMessage = message(role, text, { toolCalls });
                    messages.push(responseMessage);
                    historySignatures.push(signature(responseMessage));
                }
            }
        }

        if (responseBody && Array.isArray(responseBody.output)) {
            responseBody.output.forEach((item) => {
                if (!item) return;
                if (item.type === 'function_call' || item.type === 'tool_use') {
                    const responseMessage = message('function_call', '', {
                        toolCalls: extractToolCallsList([item]),
                    });
                    messages.push(responseMessage);
                    historySignatures.push(signature(responseMessage));
                    return;
                }
                const role = (item.role || 'assistant').toLowerCase();
                const text = extractResponsesOutputText(item);
                if (text) {
                    const responseMessage = message(role, text);
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
            const toolCalls = extractContentToolCalls(responseBody.content);
            if (text || toolCalls.length > 0) {
                const responseMessage = message(role, text, { toolCalls });
                messages.push(responseMessage);
                historySignatures.push(signature(responseMessage));
            }
        }

        const errMsg = extractConversationErrorMessage(entry);
        if (errMsg) {
            messages.push(message('error', errMsg));
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
            return tc.name + '(' + formatFunctionArguments(tc) + ')';
        }).join('\n\n');
    }
    return msg.text || '';
}

export function conversationMessageCopyText(msg) {
    if (!msg) return '';
    const text = String(msg.text || '');
    const toolCalls = Array.isArray(msg.toolCalls) && msg.toolCalls.length > 0
        ? functionExpandedContent({ role: 'function_call', toolCalls: msg.toolCalls })
        : '';
    return [text, toolCalls].filter(Boolean).join('\n\n');
}

export function formatFunctionArguments(toolCall) {
    const value = toolCall && toolCall.arguments !== undefined ? toolCall.arguments : '';
    if (typeof value !== 'string') {
        try { return JSON.stringify(value, null, 2); } catch { return String(value); }
    }
    try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; }
}
