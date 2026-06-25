import fs from "node:fs";

const file = process.argv[2] || "docs/openapi.json";
const spec = JSON.parse(fs.readFileSync(file, "utf8"));

// parseServers emits a single templated OpenAPI 3 server entry whose URL is a
// free-text variable. Mintlify renders this variable as an editable input at
// the top of every API Reference page, so visitors can point the docs at
// their own GoModel deployment without leaving the page. The first URL in
// DOCS_API_SERVERS is used as the default; any additional URLs become
// description hints so common deployments stay discoverable.
function parseServers(value) {
  const urls = (value || "")
    .split(",")
    .map((url) => url.trim())
    .filter(Boolean);
  if (urls.length === 0) {
    throw new Error("DOCS_API_SERVERS must include at least one URL");
  }
  const [defaultURL, ...alternatives] = urls;
  const description = alternatives.length === 0
    ? "Your GoModel deployment URL"
    : `Your GoModel deployment URL (e.g. ${alternatives.join(", ")})`;
  return [
    {
      url: "{base_url}",
      description: "Edit the base URL to point at your GoModel deployment.",
      variables: {
        base_url: {
          default: defaultURL,
          description,
        },
      },
    },
  ];
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function schema(name) {
  const result = spec.components?.schemas?.[name];
  if (!result) {
    throw new Error(`missing OpenAPI schema: ${name}`);
  }
  return result;
}

function applyResponseInputOneOf(name) {
  const properties = schema(name).properties;
  if (!properties?.input) {
    throw new Error(`missing input property on schema: ${name}`);
  }

  const input = {};
  if (properties.input.description) {
    input.description = properties.input.description;
  }
  input.oneOf = clone([
    { type: "string" },
    {
      type: "array",
      items: { $ref: "#/components/schemas/core.ResponsesInputElement" },
    },
  ]);
  properties.input = input;
}

function applyResponseConversationOneOf(name) {
  const properties = schema(name).properties;
  if (!properties?.conversation) {
    throw new Error(`missing conversation property on schema: ${name}`);
  }

  const conversation = {};
  if (properties.conversation.description) {
    conversation.description = properties.conversation.description;
  }
  conversation.oneOf = clone([
    { type: "string" },
    { $ref: "#/components/schemas/core.ResponsesConversationRef" },
  ]);
  properties.conversation = conversation;
}

function ensureResponsesInputElementSchema() {
  const schemas = spec.components?.schemas;
  if (!schemas) {
    throw new Error("missing OpenAPI components.schemas");
  }
  if (schemas["core.ResponsesInputElement"]) {
    return;
  }
  schemas["core.ResponsesInputElement"] = {
    type: "object",
    properties: {
      arguments: { type: "string" },
      call_id: {
        description: 'Function call fields (type="function_call")',
        type: "string",
      },
      content: {
        description: "Can be string or []ContentPart",
        oneOf: [
          { type: "string" },
          {
            type: "array",
            items: { $ref: "#/components/schemas/core.ContentPart" },
          },
        ],
      },
      name: { type: "string" },
      output: {
        description: 'Function call output fields (type="function_call_output") - CallID shared above',
        type: "string",
      },
      role: {
        description: 'Message fields (type="" or "message")',
        type: "string",
      },
      status: { type: "string" },
      type: {
        description: '"message", "function_call", "function_call_output"',
        type: "string",
      },
    },
  };
}

function anthropicContentSchema() {
  return {
    oneOf: [
      { type: "string" },
      {
        type: "array",
        items: { $ref: "#/components/schemas/anthropicapi.ContentBlock" },
      },
    ],
  };
}

function freeFormObjectSchema() {
  return {
    type: "object",
    additionalProperties: true,
  };
}

function stringOrFreeFormObjectSchema() {
  return {
    oneOf: [
      { type: "string" },
      freeFormObjectSchema(),
    ],
  };
}

function ensureAnthropicContentBlockSchema() {
  const schemas = spec.components?.schemas;
  if (!schemas) {
    throw new Error("missing OpenAPI components.schemas");
  }
  schemas["anthropicapi.ContentBlock"] = {
    type: "object",
    properties: {
      content: anthropicContentSchema(),
      id: { type: "string" },
      input: freeFormObjectSchema(),
      is_error: { type: "boolean" },
      name: { type: "string" },
      source: stringOrFreeFormObjectSchema(),
      text: { type: "string" },
      thinking: { type: "string" },
      tool_use_id: { type: "string" },
      type: { type: "string" },
    },
  };
}

function applyAnthropicMessageSchemas() {
  ensureAnthropicContentBlockSchema();
  schema("anthropicapi.Message").properties.content = anthropicContentSchema();
  schema("anthropicapi.MessagesRequest").properties.system = anthropicContentSchema();
  schema("anthropicapi.ResponseContentBlock").properties.input = freeFormObjectSchema();
  schema("anthropicapi.Tool").properties.input_schema = freeFormObjectSchema();
}

function constStringSchema(value) {
  return {
    type: "string",
    enum: [value],
  };
}

function ensureAnthropicSSEEventSchemas() {
  const schemas = spec.components?.schemas;
  if (!schemas) {
    throw new Error("missing OpenAPI components.schemas");
  }
  schemas["anthropicapi.SSEMessageStartEvent"] = {
    type: "object",
    required: ["message", "type"],
    properties: {
      type: constStringSchema("message_start"),
      message: { $ref: "#/components/schemas/anthropicapi.MessagesResponse" },
    },
  };
  schemas["anthropicapi.SSEContentBlockStartEvent"] = {
    type: "object",
    required: ["content_block", "index", "type"],
    properties: {
      type: constStringSchema("content_block_start"),
      index: { type: "integer" },
      content_block: { $ref: "#/components/schemas/anthropicapi.ResponseContentBlock" },
    },
  };
  schemas["anthropicapi.SSEContentBlockDeltaEvent"] = {
    type: "object",
    required: ["delta", "index", "type"],
    properties: {
      type: constStringSchema("content_block_delta"),
      index: { type: "integer" },
      delta: freeFormObjectSchema(),
    },
  };
  schemas["anthropicapi.SSEContentBlockStopEvent"] = {
    type: "object",
    required: ["index", "type"],
    properties: {
      type: constStringSchema("content_block_stop"),
      index: { type: "integer" },
    },
  };
  schemas["anthropicapi.SSEMessageDeltaEvent"] = {
    type: "object",
    required: ["delta", "type"],
    properties: {
      type: constStringSchema("message_delta"),
      delta: freeFormObjectSchema(),
      usage: { $ref: "#/components/schemas/anthropicapi.Usage" },
    },
  };
  schemas["anthropicapi.SSEMessageStopEvent"] = {
    type: "object",
    required: ["type"],
    properties: {
      type: constStringSchema("message_stop"),
    },
  };
  schemas["anthropicapi.SSEPingEvent"] = {
    type: "object",
    required: ["type"],
    properties: {
      type: constStringSchema("ping"),
    },
  };
  schemas["anthropicapi.SSEErrorEvent"] = {
    type: "object",
    required: ["error", "type"],
    properties: {
      type: constStringSchema("error"),
      error: { $ref: "#/components/schemas/anthropicapi.ErrorObject" },
    },
  };
  schemas["anthropicapi.SSEUnknownEvent"] = {
    description: "Fallback for future Anthropic streaming event payloads.",
    type: "object",
    required: ["type"],
    properties: {
      type: { type: "string" },
    },
    additionalProperties: true,
  };
  schemas["anthropicapi.SSEEventFrame"] = {
    description: "One server-sent event frame emitted by streaming /v1/messages. On the wire each frame is sent as event: <name> and data: <JSON payload>. Unknown event payloads are accepted for forward compatibility.",
    anyOf: [
      { $ref: "#/components/schemas/anthropicapi.SSEMessageStartEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEContentBlockStartEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEContentBlockDeltaEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEContentBlockStopEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEMessageDeltaEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEMessageStopEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEPingEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEErrorEvent" },
      { $ref: "#/components/schemas/anthropicapi.SSEUnknownEvent" },
    ],
  };
}

function applyAnthropicMessagesStreamSchema() {
  ensureAnthropicSSEEventSchemas();
  const streamResponse = spec.paths?.["/v1/messages"]?.post?.responses?.["200"]?.content?.["text/event-stream"];
  if (!streamResponse) {
    throw new Error("missing OpenAPI text/event-stream response: POST /v1/messages 200");
  }
  streamResponse.schema = {
    $ref: "#/components/schemas/anthropicapi.SSEEventFrame",
  };
}

function applyAudioTranscriptionTextSchema() {
  // swag emits one schema across every produced content type, so the text/plain
  // transcription body is generated as a JSON object. For text/srt/vtt the
  // endpoint returns a plain string, so model it as such.
  const textResponse = spec.paths?.["/v1/audio/transcriptions"]?.post?.responses?.["200"]?.content?.["text/plain"];
  if (!textResponse) {
    throw new Error("missing OpenAPI text/plain response: POST /v1/audio/transcriptions 200");
  }
  textResponse.schema = { type: "string" };
}

function ensureBearerAuthSecurityScheme() {
  const securitySchemes = spec.components?.securitySchemes;
  if (!securitySchemes?.BearerAuth) {
    throw new Error("missing OpenAPI security scheme: BearerAuth");
  }
  securitySchemes.BearerAuth = {
    type: "http",
    scheme: "bearer",
    bearerFormat: "JWT",
  };
}

function ensureRequiredProperty(schemaName, propertyName) {
  const target = schema(schemaName);
  if (!target.properties?.[propertyName]) {
    throw new Error(`missing ${propertyName} property on schema: ${schemaName}`);
  }
  const required = new Set(target.required || []);
  required.add(propertyName);
  target.required = Array.from(required).sort();
}

function applyArrayMaxItems(operationPath, method, statusCode, maxItems) {
  const op = spec.paths?.[operationPath]?.[method];
  if (!op) {
    throw new Error(`missing OpenAPI operation: ${method.toUpperCase()} ${operationPath}`);
  }
  const response = op.responses?.[statusCode];
  if (!response) {
    throw new Error(`missing response ${statusCode} on ${method.toUpperCase()} ${operationPath}`);
  }
  const schemaRef = response.content?.["application/json"]?.schema || response.schema;
  if (!schemaRef || schemaRef.type !== "array") {
    throw new Error(`expected array schema on ${method.toUpperCase()} ${operationPath} ${statusCode}`);
  }
  schemaRef.maxItems = maxItems;
  if (!schemaRef.description) {
    schemaRef.description = `Bounded by maxItems=${maxItems}.`;
  }
}

function applyStringEnum(schemaName, values, varnames) {
  const target = schema(schemaName);
  target.type = "string";
  target.enum = values;
  if (varnames) {
    target["x-enum-varnames"] = varnames;
  }
}

function applyStringArrayPropertyBounds(schemaName, propertyName, maxItems, itemMaxLength) {
  const target = schema(schemaName);
  const property = target.properties?.[propertyName];
  if (!property || property.type !== "array") {
    throw new Error(`expected array property ${propertyName} on schema: ${schemaName}`);
  }
  property.maxItems = maxItems;
  property.items = property.items || {};
  property.items.maxLength = itemMaxLength;
}

// applyPathSidebarTitles sets the API Reference sidebar entry of every
// operation to its path so endpoints are listed by URL rather than by
// natural-language summary. Mintlify renders the HTTP method as a colored
// pill from the spec, so the title itself omits the method to avoid
// duplicating it.
function applyPathSidebarTitles() {
  const httpMethods = new Set([
    "get",
    "post",
    "put",
    "patch",
    "delete",
    "head",
    "options",
    "trace",
  ]);
  for (const [path, pathItem] of Object.entries(spec.paths || {})) {
    if (!pathItem || typeof pathItem !== "object") continue;
    for (const [method, operation] of Object.entries(pathItem)) {
      if (!httpMethods.has(method)) continue;
      if (!operation || typeof operation !== "object") continue;
      operation["x-mint"] ??= {};
      operation["x-mint"].metadata ??= {};
      operation["x-mint"].metadata.sidebarTitle = path;
    }
  }
}

function applyPricingSchemaConstraints() {
  schema("pricingoverrides.Pricing").minProperties = 1;
  for (const name of ["core.ModelPricingTier", "pricingoverrides.PricingTier"]) {
    const upToTokens = schema(name).properties?.up_to_tokens;
    if (!upToTokens) {
      throw new Error(`missing up_to_tokens property on schema: ${name}`);
    }
    upToTokens.type = "integer";
    upToTokens.minimum = 1;
  }
}

function applyBudgetKeySchemaConstraints() {
  const target = schema("admin.budgetKeyRequest");
  if (!target.properties?.period || !target.properties?.period_seconds) {
    throw new Error("missing budget key period properties");
  }
  target.oneOf = [{ required: ["period"] }, { required: ["period_seconds"] }];
}

spec.servers = parseServers(process.env.DOCS_API_SERVERS);
ensureResponsesInputElementSchema();
applyAnthropicMessageSchemas();
applyAnthropicMessagesStreamSchema();
applyAudioTranscriptionTextSchema();
ensureBearerAuthSecurityScheme();
ensureRequiredProperty("core.AudioSpeechRequest", "model");
ensureRequiredProperty("core.AudioSpeechRequest", "input");
ensureRequiredProperty("core.AudioSpeechRequest", "voice");
ensureRequiredProperty("admin.recalculatePricingRequest", "confirmation");
ensureRequiredProperty("admin.upsertBudgetRequest", "amount");
ensureRequiredProperty("admin.upsertBudgetRequest", "budget_key");
ensureRequiredProperty("admin.deleteBudgetRequest", "budget_key");
ensureRequiredProperty("admin.upsertVirtualModelRequest", "source");
ensureRequiredProperty("admin.deleteVirtualModelRequest", "source");
ensureRequiredProperty("admin.upsertModelPricingOverrideRequest", "selector");
ensureRequiredProperty("admin.upsertModelPricingOverrideRequest", "pricing");
ensureRequiredProperty("admin.deleteModelPricingOverrideRequest", "selector");
ensureRequiredProperty("core.ResponsesConversationRef", "id");
applyBudgetKeySchemaConstraints();
applyStringArrayPropertyBounds("admin.upsertVirtualModelRequest", "user_paths", 100, 1024);
applyPricingSchemaConstraints();
applyPathSidebarTitles();

// Bound the registry-backed admin model listing so OpenAPI consumers (and
// security scanners like CKV_OPENAPI_21) see an explicit upper limit. The
// runtime registry is bounded by configured providers and the backing
// model list; 10000 leaves substantial headroom for that worst case.
applyArrayMaxItems("/admin/models", "get", "200", 10000);
applyArrayMaxItems("/admin/virtual-models", "get", "200", 10000);
applyArrayMaxItems("/admin/model-pricing-overrides", "get", "200", 10000);

applyStringEnum(
  "pricingoverrides.ScopeKind",
  ["global", "model", "provider", "provider_model"],
  ["PricingScopeGlobal", "PricingScopeModel", "PricingScopeProvider", "PricingScopeProviderModel"],
);

for (const name of [
  "core.ResponsesRequest",
  "core.ResponseInputTokensRequest",
  "core.ResponseCompactRequest",
]) {
  applyResponseInputOneOf(name);
  applyResponseConversationOneOf(name);
}

const inputItemList = schema("core.ResponseInputItemListResponse");
if (!inputItemList.properties?.data) {
  throw new Error("missing data property on schema: core.ResponseInputItemListResponse");
}
inputItemList.properties.data = {
  type: "array",
  items: { $ref: "#/components/schemas/core.ResponsesInputElement" },
};

fs.writeFileSync(file, `${JSON.stringify(spec, null, 2)}\n`);
