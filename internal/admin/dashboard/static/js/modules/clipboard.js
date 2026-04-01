(function(global) {
    function fallbackClipboardModule(scope) {
        function getScheduler(name, fallback) {
            if (scope && typeof scope[name] === 'function') {
                return scope[name].bind(scope);
            }
            if (typeof fallback === 'function') {
                return fallback;
            }
            return null;
        }

        function writeTextToClipboard(value) {
            const payload = String(value == null ? '' : value);
            const clipboard = scope.navigator && scope.navigator.clipboard;

            if (clipboard && typeof clipboard.writeText === 'function') {
                return clipboard.writeText(payload);
            }

            const doc = scope.document;
            if (!doc || !doc.body || typeof doc.createElement !== 'function' || typeof doc.execCommand !== 'function') {
                return Promise.reject(new Error('Clipboard API unavailable'));
            }

            const textarea = doc.createElement('textarea');
            textarea.value = payload;
            textarea.setAttribute('readonly', '');
            textarea.style.position = 'fixed';
            textarea.style.top = '0';
            textarea.style.left = '0';
            textarea.style.opacity = '0';

            try {
                doc.body.appendChild(textarea);
                if (typeof textarea.focus === 'function') {
                    textarea.focus();
                }
                if (typeof textarea.select === 'function') {
                    textarea.select();
                }
                if (typeof textarea.setSelectionRange === 'function') {
                    textarea.setSelectionRange(0, textarea.value.length);
                }

                if (!doc.execCommand('copy')) {
                    throw new Error('execCommand copy returned false');
                }
            } finally {
                if (textarea.parentNode) {
                    textarea.parentNode.removeChild(textarea);
                }
            }

            return Promise.resolve();
        }

        function createClipboardButtonState(options = {}) {
            const setTimer = getScheduler('setTimeout', typeof setTimeout === 'function' ? setTimeout : null);
            const clearTimer = getScheduler('clearTimeout', typeof clearTimeout === 'function' ? clearTimeout : null);
            const resetDelayMs = Number.isFinite(options.resetDelayMs) ? options.resetDelayMs : 2000;

            return {
                copied: false,
                error: false,
                resetTimer: null,

                clearResetTimer() {
                    if (this.resetTimer !== null && clearTimer) {
                        clearTimer(this.resetTimer);
                    }
                    this.resetTimer = null;
                },

                scheduleReset() {
                    this.clearResetTimer();
                    if (!setTimer) {
                        return;
                    }
                    this.resetTimer = setTimer(() => {
                        this.copied = false;
                        this.error = false;
                        this.resetTimer = null;
                    }, resetDelayMs);
                },

                resetFeedback() {
                    this.clearResetTimer();
                    this.copied = false;
                    this.error = false;
                },

                setFeedback(copied, error) {
                    this.copied = copied;
                    this.error = error;
                    this.scheduleReset();
                },

                async copy(value, formatValue) {
                    if (value == null || value === undefined || value === '') {
                        return;
                    }

                    this.resetFeedback();

                    try {
                        const payload = typeof formatValue === 'function'
                            ? formatValue(value)
                            : String(value);
                        await writeTextToClipboard(payload);
                        this.setFeedback(true, false);
                    } catch (error) {
                        console.error(options.logPrefix || 'Failed to copy text:', error);
                        this.setFeedback(false, true);
                    }
                }
            };
        }

        return {
            writeTextToClipboard,
            createClipboardButtonState
        };
    }

    function dashboardClipboardModule() {
        return fallbackClipboardModule(global);
    }

    global.dashboardClipboardModule = dashboardClipboardModule;
})(window);
