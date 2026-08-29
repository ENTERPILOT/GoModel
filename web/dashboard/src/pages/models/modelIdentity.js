// Model identity: how a model, a selector and a virtual model name are matched to one another.
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

export const GLOBAL_OVERRIDE_SELECTOR = "/";

// ---- Naming / identity helpers ----

export function normalizedAliasName(name) {
  return String(name || "")
    .trim()
    .toLowerCase();
}

export function qualifiedModelName(model) {
  if (!model) {
    return "";
  }
  const selector = String(model.selector || "").trim();
  if (selector) {
    return selector;
  }
  if (!model.model || !model.model.id) {
    return "";
  }
  const modelID = String(model.model.id || "").trim();
  const providerName = String(model.provider_name || "").trim();
  if (providerName) {
    return providerName + "/" + modelID;
  }
  const providerType = String(model.provider_type || "").trim();
  if (!providerType || modelID.includes("/")) {
    return modelID;
  }
  return providerType + "/" + modelID;
}

function modelIdentifierKeys(modelID, providerType, providerName, selector) {
  const keys = new Set();
  const normalizedModelID = String(modelID || "")
    .trim()
    .toLowerCase();
  const provider = String(providerType || "")
    .trim()
    .toLowerCase();
  const providerLabel = String(providerName || "")
    .trim()
    .toLowerCase();
  const normalizedSelector = String(selector || "")
    .trim()
    .toLowerCase();
  if (normalizedSelector) {
    keys.add(normalizedSelector);
  }
  if (!normalizedModelID) {
    return keys;
  }

  keys.add(normalizedModelID);
  if (providerLabel) {
    keys.add(providerLabel + "/" + normalizedModelID);
  }
  if (provider && !normalizedModelID.includes("/")) {
    keys.add(provider + "/" + normalizedModelID);
  }

  const parts = normalizedModelID.split("/");
  if (parts.length === 2 && parts[1]) {
    keys.add(parts[1]);
  }

  return keys;
}

export function modelKeys(model) {
  return modelIdentifierKeys(
    model && model.model ? model.model.id : "",
    model ? model.provider_type : "",
    model ? model.provider_name : "",
    model ? model.selector : "",
  );
}

export function aliasKeys(alias) {
  const keys = new Set();
  const resolved = String(alias.resolved_model || "")
    .trim()
    .toLowerCase();
  const targetModel = String(alias.target_model || "")
    .trim()
    .toLowerCase();
  const targetProvider = String(alias.target_provider || "")
    .trim()
    .toLowerCase();
  if (resolved) {
    keys.add(resolved);
    const resolvedParts = resolved.split("/");
    if (resolvedParts.length === 2 && resolvedParts[1]) {
      keys.add(resolvedParts[1]);
    }
  }
  if (targetModel) {
    keys.add(targetModel);
    const targetParts = targetModel.split("/");
    if (targetParts.length === 2 && targetParts[1]) {
      keys.add(targetParts[1]);
    }
  }
  if (targetModel && targetProvider) {
    keys.add(targetProvider + "/" + targetModel);
  }
  return keys;
}
