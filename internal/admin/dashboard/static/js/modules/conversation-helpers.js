(function(global) {
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
            } catch (_) {
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

    function extractRequestPromptTextSegments(body) {
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
        } catch (_) {
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

    function extractConversationErrorMessage(entry) {
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

    function canShowConversation(entry) {
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

    function jsonStringContent(value) {
        try {
            return JSON.stringify(String(value)).slice(1, -1);
        } catch (_) {
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

    function renderBodyWithConversationHighlights(entry, value, deps) {
        const formatJSON = deps && typeof deps.formatJSON === 'function' ? deps.formatJSON : (v) => String(v);
        const canShow = deps && typeof deps.canShowConversation === 'function' ? deps.canShowConversation : () => false;
        const promptCacheState = createPromptCacheHighlightState(deps && deps.promptCacheHighlight);

        const raw = formatJSON(value);
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

    global.DashboardConversationHelpers = {
        extractText,
        extractRequestPromptTextSegments,
        extractResponsesInputMessages,
        extractResponsesOutputText,
        extractConversationErrorMessage,
        looksLikeResponsesOutput,
        hasConversationPayload,
        isConversationalPath,
        isConversationExcludedPath,
        canShowConversation,
        renderBodyWithConversationHighlights
    };
})(window);
