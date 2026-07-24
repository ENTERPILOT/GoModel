// Pure conversation/message shaping helpers, ported verbatim from
// internal/admin/dashboard/static/js/modules/conversation-helpers.js, plus
// the pure message-building logic extracted from conversation-drawer.js
// (buildConversationMessages, functionExpandedContent) and the formatJSON
// helper the body renderer depends on (from audit-list.js).
// Plain ESM — no runes, no imports — shared by the Svelte stores and tests.

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

function tryParseJSON(value) {
    if (typeof value !== 'string') return null;
    try {
        return JSON.parse(value);
    } catch {
        return null;
    }
}

function normalizeErrorMessageText(text, depth) {
    const trimmed = String(text || '').trim();
    if (!trimmed) return '';
    if (depth >= 4) return trimmed;

    const parsed = tryParseJSON(trimmed);
    if (!parsed || typeof parsed !== 'object') {
        return trimmed;
    }

    const nested = findNestedErrorMessage(parsed, depth + 1);
    if (nested) return nested;

    const fallback = extractText(parsed);
    return fallback || trimmed;
}

function findNestedErrorMessage(value, depth = 0) {
    const visited = new Set();
    const stack = [value];

    while (stack.length > 0) {
        const current = stack.shift();
        if (!current || typeof current !== 'object') continue;
        if (visited.has(current)) continue;
        visited.add(current);

        if (Array.isArray(current)) {
            for (let i = 0; i < current.length; i++) {
                stack.push(current[i]);
            }
            continue;
        }

        const error = current.error;
        if (typeof error === 'string' && error.trim()) {
            return normalizeErrorMessageText(error, depth);
        }
        if (error && typeof error === 'object') {
            if (typeof error.message === 'string' && error.message.trim()) {
                return normalizeErrorMessageText(error.message, depth);
            }
            stack.push(error);
        }

        if (typeof current.message === 'string' && current.message.trim()) {
            if (current.error !== undefined || current.code !== undefined || current.status !== undefined || current.type !== undefined) {
                return normalizeErrorMessageText(current.message, depth);
            }
        }

        const keys = Object.keys(current);
        for (let i = 0; i < keys.length; i++) {
            const key = keys[i];
            if (key === 'error') continue;
            stack.push(current[key]);
        }
    }

    return '';
}

export function extractConversationErrorMessage(entry) {
    if (!entry || !entry.data) return '';

    const responseBodyMessage = findNestedErrorMessage(entry.data.response_body);
    if (responseBodyMessage) return responseBodyMessage;

    const rawError = entry.data.error_message;
    if (rawError == null) return '';

    if (typeof rawError === 'string') {
        const trimmed = rawError.trim();
        if (!trimmed) return '';

        const parsed = tryParseJSON(trimmed);
        const parsedMessage = findNestedErrorMessage(parsed);
        if (parsedMessage) return parsedMessage;
        return trimmed;
    }

    const structuredMessage = findNestedErrorMessage(rawError);
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
    const p = String(path).toLowerCase();
    return p === '/v1/chat/completions' ||
        p === '/v1/chat/completions/' ||
        p.startsWith('/v1/chat/completions?') ||
        p.startsWith('/v1/chat/completions/') ||
        p === '/v1/responses' ||
        p === '/v1/responses/' ||
        p.startsWith('/v1/responses?') ||
        p.startsWith('/v1/responses/');
}

function hasConversationPayload(entry) {
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

// formatJSON pretty-prints a captured body value ("Not captured" for empty);
// the body renderer above and the drawer store use it as the default
// formatter.
export function formatJSON(v) {
    if (v == null || v === undefined || v === '') return 'Not captured';

    if (typeof v === 'string') {
        const trimmed = v.trim();
        if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
            try {
                return JSON.stringify(JSON.parse(trimmed), null, 2);
            } catch {
                return v;
            }
        }
        return v;
    }

    try {
        return JSON.stringify(v, null, 2);
    } catch {
        return String(v);
    }
}

// ---------------------------------------------------------------------------
// Message building (extracted from conversation-drawer.js so the drawer store
// stays thin and the logic is testable without runes).
// ---------------------------------------------------------------------------

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

function conversationMessage(role, text, timestamp, entryID, isAnchor, idx, toolCalls, functionName) {
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

export function buildConversationMessages(entries, anchorID) {
    if (!Array.isArray(entries) || entries.length === 0) return [];

    const sorted = [...entries].sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));

    const callIdMap = {};
    sorted.forEach((entry) => {
        const rb = entry.data && entry.data.request_body ? entry.data.request_body : null;
        const rsb = entry.data && entry.data.response_body ? entry.data.response_body : null;
        collectCallIds(callIdMap, rb, rsb);
    });

    const messages = [];
    let idx = 0;

    sorted.forEach((entry) => {
        const isAnchor = entry.id === anchorID;
        const ts = entry.timestamp;
        const requestBody = entry.data && entry.data.request_body ? entry.data.request_body : null;
        const responseBody = entry.data && entry.data.response_body ? entry.data.response_body : null;

        if (requestBody && typeof requestBody.instructions === 'string' && requestBody.instructions.trim()) {
            messages.push(conversationMessage('system', requestBody.instructions, ts, entry.id, isAnchor, ++idx));
        }

        if (requestBody && Array.isArray(requestBody.messages)) {
            requestBody.messages.forEach((m) => {
                if (!m) return;
                const role = (m.role || 'user').toLowerCase();
                if (role === 'tool') {
                    const text = extractText(m.content);
                    const fnName = m.name || callIdMap[m.tool_call_id] || '';
                    if (text) messages.push(conversationMessage('function_result', text, ts, entry.id, isAnchor, ++idx, [], fnName));
                    return;
                }
                if (role === 'assistant') {
                    const text = extractText(m.content);
                    const toolCalls = extractToolCallsList(m.tool_calls);
                    if (text || toolCalls.length > 0) {
                        messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, ++idx, toolCalls));
                    }
                    return;
                }
                const text = extractText(m.content);
                if (text) messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, ++idx));
            });
        }

        if (requestBody && requestBody.input !== undefined) {
            if (Array.isArray(requestBody.input)) {
                requestBody.input.forEach((item) => {
                    if (!item || typeof item !== 'object') return;
                    if (item.type === 'function_call_output') {
                        const text = typeof item.output === 'string' ? item.output : extractText(item.output);
                        if (text) messages.push(conversationMessage('function_result', text, ts, entry.id, isAnchor, ++idx, [], callIdMap[item.call_id] || ''));
                    } else if (item.type === 'function_call') {
                        messages.push(conversationMessage('function_call', '', ts, entry.id, isAnchor, ++idx, [{ name: item.name || '', arguments: item.arguments || '' }]));
                    } else if (item.role) {
                        const role = String(item.role).toLowerCase();
                        const text = extractText(item.content);
                        if (text) messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, ++idx));
                    }
                });
            } else {
                extractResponsesInputMessages(requestBody.input).forEach((m) => {
                    if (m.text) messages.push(conversationMessage(m.role, m.text, ts, entry.id, isAnchor, ++idx));
                });
            }
        }

        if (responseBody && Array.isArray(responseBody.choices)) {
            const first = responseBody.choices[0];
            if (first && first.message) {
                const role = (first.message.role || 'assistant').toLowerCase();
                const text = extractText(first.message.content);
                const toolCalls = extractToolCallsList(first.message.tool_calls);
                if (text || toolCalls.length > 0) {
                    messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, ++idx, toolCalls));
                }
            }
        }

        if (responseBody && Array.isArray(responseBody.output)) {
            responseBody.output.forEach((item) => {
                if (!item) return;
                if (item.type === 'function_call') {
                    messages.push(conversationMessage('function_call', '', ts, entry.id, isAnchor, ++idx, [{ name: item.name || '', arguments: item.arguments || '' }]));
                    return;
                }
                const role = (item.role || 'assistant').toLowerCase();
                const text = extractResponsesOutputText(item);
                if (text) messages.push(conversationMessage(role, text, ts, entry.id, isAnchor, ++idx));
            });
        }

        const errMsg = extractConversationErrorMessage(entry);
        if (errMsg) {
            messages.push(conversationMessage('error', errMsg, ts, entry.id, isAnchor, ++idx));
        }
    });

    return messages;
}

// functionExpandedContent renders the expandable body of a function-call /
// function-result note (pretty-printing JSON arguments when possible).
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
