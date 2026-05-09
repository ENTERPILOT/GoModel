(function(global) {
    function dashboardPricingModule() {
        return {
            pricingRecalculateUserPath: '',
            pricingRecalculateSelector: '',
            pricingRecalculateNotice: '',
            pricingRecalculateError: '',
            pricingRecalculateLoading: false,
            pricingRecalculateDialogOpen: false,

            pricingRecalculationEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('USAGE_PRICING_RECALCULATION_ENABLED', false)
                    : false;
            },

            pricingRecalculateDatePayload() {
                if (this.selectedPreset) {
                    return { days: parseInt(this.selectedPreset, 10) || 30 };
                }
                const start = this.customStartDate ? this._formatDate(this.customStartDate) : '';
                const endDate = this.customEndDate || (typeof this.todayDate === 'function' ? this.todayDate() : null);
                const end = endDate ? this._formatDate(endDate) : '';
                return {
                    start_date: start,
                    end_date: end
                };
            },

            pricingRecalculatePayload(confirmation) {
                return {
                    ...this.pricingRecalculateDatePayload(),
                    user_path: String(this.pricingRecalculateUserPath || '').trim(),
                    selector: String(this.pricingRecalculateSelector || '').trim(),
                    confirmation
                };
            },

            openPricingRecalculateDialog() {
                if (!this.pricingRecalculationEnabled()) {
                    this.pricingRecalculateError = 'Usage pricing recalculation is unavailable.';
                    return;
                }
                if (this.pricingRecalculateLoading) {
                    return;
                }
                this.pricingRecalculateError = '';
                this.pricingRecalculateDialogOpen = true;
                if (typeof this.openTypedConfirmationDialog === 'function') {
                    this.openTypedConfirmationDialog({
                        title: 'Recalculate Pricing',
                        titleId: 'pricingRecalculateDialogTitle',
                        inputId: 'pricing-recalculate-confirmation',
                        requiredText: 'recalculate',
                        confirmLabel: 'Recalculate Pricing',
                        icon: 'calculator',
                        dialogClass: 'pricing-recalculate-dialog',
                        loadingKey: 'pricingRecalculateLoading',
                        errorKey: 'pricingRecalculateError',
                        message: 'Stored usage cost fields matching the selected filters will be overwritten.',
                        onConfirm: () => this.recalculatePricing(),
                        onClose: () => {
                            this.pricingRecalculateDialogOpen = false;
                        }
                    });
                }
            },

            closePricingRecalculateDialog() {
                if (this.typedConfirmationDialog && this.typedConfirmationDialog.open && typeof this.closeTypedConfirmationDialog === 'function') {
                    this.closeTypedConfirmationDialog();
                    return;
                }
                this.pricingRecalculateDialogOpen = false;
            },

            pricingRecalculateConfirmationValue() {
                if (this.typedConfirmationDialog && this.typedConfirmationDialog.open) {
                    return this.typedConfirmationDialog.value;
                }
                return '';
            },

            async recalculatePricing() {
                if (!this.pricingRecalculationEnabled()) {
                    this.pricingRecalculateError = 'Usage pricing recalculation is unavailable.';
                    return;
                }
                if (this.pricingRecalculateLoading) {
                    return;
                }
                const confirmation = this.pricingRecalculateConfirmationValue();
                if (String(confirmation || '').trim().toLowerCase() !== 'recalculate') {
                    this.pricingRecalculateError = 'Type recalculate to confirm.';
                    return;
                }

                this.pricingRecalculateLoading = true;
                this.pricingRecalculateNotice = '';
                this.pricingRecalculateError = '';
                try {
                    const request = this.requestOptions({
                        method: 'POST',
                        body: JSON.stringify(this.pricingRecalculatePayload('recalculate'))
                    });
                    const res = await fetch('/admin/usage/recalculate-pricing', request);
                    const handled = this.handleFetchResponse(res, 'pricing recalculation', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.pricingRecalculateError = 'Unable to recalculate pricing.';
                        return;
                    }

                    const result = await res.json();
                    this.closePricingRecalculateDialog();
                    this.pricingRecalculateNotice = this.pricingRecalculateSummary(result);
                    await this.refreshAfterPricingRecalculate();
                } catch (e) {
                    console.error('Failed to recalculate pricing:', e);
                    this.pricingRecalculateError = 'Unable to recalculate pricing.';
                } finally {
                    this.pricingRecalculateLoading = false;
                }
            },

            pricingRecalculateSummary(result) {
                const matched = Number(result && result.matched || 0);
                const recalculated = Number(result && result.recalculated || 0);
                const withoutPricing = Number(result && result.without_pricing || 0);
                let message = 'Pricing recalculated for ' + recalculated + ' of ' + matched + ' usage record' + (matched === 1 ? '' : 's') + '.';
                if (withoutPricing > 0) {
                    message += ' ' + withoutPricing + ' usage record' + (withoutPricing === 1 ? ' still lacks' : 's still lack') + ' pricing metadata.';
                }
                return message;
            },

            async refreshAfterPricingRecalculate() {
                const requests = [];
                if (typeof this.fetchUsage === 'function') {
                    requests.push(this.fetchUsage());
                }
                if (this.page === 'usage' && typeof this.fetchUsagePage === 'function') {
                    requests.push(this.fetchUsagePage());
                }
                if (this.page === 'budgets' && typeof this.fetchBudgets === 'function') {
                    requests.push(this.fetchBudgets());
                }
                if (requests.length > 0) {
                    await Promise.all(requests);
                }
            }
        };
    }

    global.dashboardPricingModule = dashboardPricingModule;
})(typeof window !== 'undefined' ? window : globalThis);
