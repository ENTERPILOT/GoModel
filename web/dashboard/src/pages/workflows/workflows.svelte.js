// Workflows page state: list fetch, editor form, CRUD against
// /admin/workflows. Pure form/scope/payload rules live in workflowsLogic.js;
// runtime feature gates come from the shared runtimeConfig store.

import { errorMessage, getJSON, isAbortError, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import {
  defaultWorkflowForm,
  emptyHydratedScope,
  defaultWorkflowGuardrailStep,
  parseWorkflowGuardrailStep,
  workflowNormalizedFeatures,
  workflowSourceFeatures,
  workflowSourceGuardrails,
  workflowScopeProviderValue,
  workflowProviderOptions,
  workflowModelOptions,
  workflowActiveScopeMatch,
  workflowDisplayName,
  workflowPreview,
  filterWorkflows,
  buildWorkflowRequest,
  validateWorkflowRequest,
  canDeactivateWorkflow,
} from "./workflowsLogic.js";

class WorkflowsStore {
  workflows = $state([]);
  available = $state(true);
  loading = $state(false);
  // Load failures only; mutation feedback goes through the flash store.
  error = $state("");
  filter = $state("");

  formOpen = $state(false);
  submitting = $state(false);
  deactivatingID = $state("");
  formError = $state("");
  formHydrated = $state(false);
  hydratedScope = $state(emptyHydratedScope());
  guardrailRefs = $state([]);
  form = $state(defaultWorkflowForm());

  #listController = null;

  // ─── Feature gates ───

  // FAILOVER_ENABLED has no dedicated helper on the runtimeConfig store.
  failoverVisible() {
    return runtimeConfig.booleanFlag("FAILOVER_ENABLED", true);
  }

  featureCaps() {
    return {
      cache: runtimeConfig.cacheVisible(),
      audit: runtimeConfig.auditVisible(),
      usage: runtimeConfig.usageVisible(),
      budget: runtimeConfig.budgetsVisible(),
      guardrails: runtimeConfig.guardrailsVisible(),
      failover: this.failoverVisible(),
    };
  }

  // ─── Derived views ───

  get filteredWorkflows() {
    return filterWorkflows(this.workflows, this.filter);
  }

  providerOptions() {
    return workflowProviderOptions(modelsStore.models, this.hydratedScope);
  }

  modelOptions(providerName) {
    return workflowModelOptions(modelsStore.models, providerName, this.hydratedScope);
  }

  activeScopeMatch() {
    return workflowActiveScopeMatch(this.workflows, this.form, this.formHydrated);
  }

  submitMode() {
    return this.activeScopeMatch() ? "save" : "create";
  }

  submitLabel() {
    return this.submitMode() === "save" ? "Save" : "Create";
  }

  submittingLabel() {
    return this.submitMode() === "save" ? "Saving..." : "Creating...";
  }

  preview() {
    return workflowPreview(this.form, this.featureCaps());
  }

  // ─── Editor form ───

  openCreate(workflow) {
    this.formOpen = true;
    this.submitting = false;
    this.formError = "";

    if (!workflow) {
      this.formHydrated = false;
      this.hydratedScope = emptyHydratedScope();
      this.form = defaultWorkflowForm();
      return;
    }

    this.formHydrated = true;
    this.hydratedScope = {
      scope_provider: workflowScopeProviderValue(workflow.scope),
      scope_model: String((workflow.scope && workflow.scope.scope_model) || "").trim(),
      scope_user_path: String(
        (workflow.scope && workflow.scope.scope_user_path) || "",
      ).trim(),
    };
    const storedFeatures =
      workflow.workflow_payload && workflow.workflow_payload.features
        ? workflowNormalizedFeatures(workflow.workflow_payload.features)
        : workflowSourceFeatures(workflow, this.featureCaps());
    const storedGuardrails = Array.isArray(
      workflow.workflow_payload && workflow.workflow_payload.guardrails,
    )
      ? workflow.workflow_payload.guardrails
          .map((step) => ({
            ref: String((step && step.ref) || "").trim(),
            step: parseWorkflowGuardrailStep(step && step.step),
          }))
          .filter((step) => Number.isInteger(step.step) && step.step >= 0)
      : workflowSourceGuardrails(workflow);
    this.form = {
      scope_provider: workflowScopeProviderValue(workflow.scope),
      scope_model: String((workflow.scope && workflow.scope.scope_model) || ""),
      scope_user_path: String((workflow.scope && workflow.scope.scope_user_path) || ""),
      name: String(workflow.name || ""),
      description: String(workflow.description || ""),
      features: {
        cache: !!storedFeatures.cache,
        audit: !!storedFeatures.audit,
        usage: !!storedFeatures.usage,
        budget: !!storedFeatures.budget,
        guardrails: !!storedFeatures.guardrails,
        failover: !!storedFeatures.failover,
      },
      guardrails: storedGuardrails.map((step) => ({
        ref: String((step && step.ref) || ""),
        step: Number.isFinite(step && step.step) ? step.step : 10,
      })),
    };
  }

  closeForm() {
    this.formOpen = false;
    this.submitting = false;
    this.formError = "";
    this.formHydrated = false;
    this.hydratedScope = emptyHydratedScope();
    this.form = defaultWorkflowForm();
  }

  setProvider(provider) {
    this.form.scope_provider = String(provider || "").trim();
    if (!this.form.scope_provider) {
      this.form.scope_model = "";
      return;
    }
    const modelOptions = this.modelOptions(this.form.scope_provider);
    if (!modelOptions.includes(String(this.form.scope_model || "").trim())) {
      this.form.scope_model = "";
    }
  }

  addGuardrailStep() {
    const steps = Array.isArray(this.form.guardrails) ? this.form.guardrails : [];
    const nextStep =
      steps.reduce((maxStep, step) => {
        const parsed = Number(step && step.step);
        return Number.isFinite(parsed) ? Math.max(maxStep, parsed) : maxStep;
      }, 0) + 10;
    this.form.guardrails.push(defaultWorkflowGuardrailStep(nextStep));
  }

  removeGuardrailStep(index) {
    if (!Array.isArray(this.form.guardrails)) return;
    this.form.guardrails.splice(index, 1);
  }

  buildRequest() {
    return buildWorkflowRequest({
      form: this.form,
      caps: this.featureCaps(),
      workflows: this.workflows,
      formHydrated: this.formHydrated,
      hydratedScope: this.hydratedScope,
    });
  }

  // ─── Fetching ───

  async fetchWorkflows() {
    if (this.#listController) this.#listController.abort();
    const controller = new AbortController();
    this.#listController = controller;
    this.loading = true;
    this.error = "";
    // 10s watchdog: a hung request surfaces a timeout error.
    const timeoutID = setTimeout(() => controller.abort(), 10000);
    try {
      const result = await getJSON("/admin/workflows", {
        label: "workflows",
        signal: controller.signal,
      });
      if (result.stale) return;
      if (result.status === 503) {
        this.available = false;
        this.workflows = [];
        return;
      }
      this.available = true;
      if (!result.ok) {
        this.workflows = [];
        return;
      }
      this.workflows = Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      // A newer fetch superseded this one: keep its state untouched.
      if (isAbortError(e) && this.#listController !== controller) return;
      console.error("Failed to fetch workflows:", e);
      this.workflows = [];
      this.error = isAbortError(e)
        ? "Loading workflows timed out."
        : "Unable to load workflows.";
    } finally {
      clearTimeout(timeoutID);
      if (this.#listController === controller) {
        this.#listController = null;
        this.loading = false;
      }
    }
  }

  async fetchGuardrailRefs() {
    try {
      const result = await getJSON("/admin/workflows/guardrails", {
        label: "workflow guardrails",
      });
      if (result.stale) return;
      this.guardrailRefs = result.ok && Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      console.error("Failed to fetch workflow guardrails:", e);
      this.guardrailRefs = [];
    }
  }

  async fetchPage() {
    await Promise.all([
      runtimeConfig.ensureLoaded(),
      this.fetchWorkflows(),
      this.fetchGuardrailRefs(),
    ]);
  }

  // ─── CRUD ───

  async submitForm() {
    if (this.submitting) {
      return;
    }
    this.formError = "";

    const payload = this.buildRequest();
    const validationError = validateWorkflowRequest(payload, {
      models: modelsStore.models,
      hydratedScope: this.hydratedScope,
    });
    if (validationError) {
      this.formError = validationError;
      return;
    }

    this.submitting = true;
    try {
      const result = await sendJSON("/admin/workflows", "POST", payload, {
        label: "create workflow",
      });
      if (result.stale || result.status === 401) {
        return;
      }
      if (!result.ok) {
        this.formError = errorMessage(result, "Unable to create workflow.");
        console.error("Failed to create workflow:", result.status, this.formError);
        return;
      }

      flash.success("Workflow created and activated.");
      this.closeForm();
      void this.fetchPage();
    } catch (e) {
      console.error("Failed to create workflow:", e);
      this.formError = "Unable to create workflow.";
    } finally {
      this.submitting = false;
    }
  }

  async deactivate(workflow) {
    const workflowID = String((workflow && workflow.id) || "").trim();
    if (!workflowID || this.deactivatingID || !canDeactivateWorkflow(workflow)) {
      return;
    }
    const workflowName = workflowDisplayName(workflow);
    if (
      !confirm(
        'Deactivate workflow "' +
          workflowName +
          '"? Requests will fall back to the next active workflow for this scope.',
      )
    ) {
      return;
    }

    this.deactivatingID = workflowID;
    try {
      const result = await sendJSON(
        "/admin/workflows/" + encodeURIComponent(workflowID) + "/deactivate",
        "POST",
        undefined,
        { label: "deactivate workflow" },
      );
      if (result.stale || result.status === 401) {
        return;
      }
      if (!result.ok) {
        const message = errorMessage(result, "Unable to deactivate workflow.");
        console.error("Failed to deactivate workflow:", result.status, message);
        flash.error(message);
        return;
      }

      flash.success("Workflow deactivated.");
      void this.fetchPage();
    } catch (e) {
      console.error("Failed to deactivate workflow:", e);
      flash.error("Unable to deactivate workflow.");
    } finally {
      this.deactivatingID = "";
    }
  }
}

export const workflowsStore = new WorkflowsStore();
