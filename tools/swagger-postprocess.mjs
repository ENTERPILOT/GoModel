import fs from "node:fs";

const file = process.argv[2] || "cmd/gomodel/docs/docs.go";
const source = fs.readFileSync(file, "utf8");
const marker = "const docTemplate = `";
const start = source.indexOf(marker);
if (start < 0) {
  throw new Error("missing docTemplate start");
}
const templateStart = start + marker.length;
const end = source.indexOf("`\n\n// SwaggerInfo", templateStart);
if (end < 0) {
  throw new Error("missing docTemplate end");
}

const schemesMarker = "__GOMODEL_SWAGGER_SCHEMES__";
const template = source.slice(templateStart, end);
const rawBacktickJoin = "` + \"`\" + `";
const parseableTemplate = template.replace(
  new RegExp(rawBacktickJoin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g"),
  "`",
).replace(
  "{{ marshal .Schemes }}",
  `["${schemesMarker}"]`,
);
const spec = JSON.parse(parseableTemplate);

function schema(name) {
  const result = spec.definitions?.[name];
  if (!result) {
    throw new Error(`missing Swagger definition: ${name}`);
  }
  return result;
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function anthropicContentSchema() {
  return {
    oneOf: [
      { type: "string" },
      {
        type: "array",
        items: { $ref: "#/definitions/anthropicapi.ContentBlock" },
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

function applyResponseConversationOneOf(name) {
  const properties = schema(name).properties;
  if (!properties?.conversation) {
    throw new Error(`missing conversation property on definition: ${name}`);
  }

  const conversation = {};
  if (properties.conversation.description) {
    conversation.description = properties.conversation.description;
  }
  conversation.oneOf = clone([
    { type: "string" },
    { $ref: "#/definitions/core.ResponsesConversationRef" },
  ]);
  properties.conversation = conversation;
}

function ensureRequiredProperty(schemaName, propertyName) {
  const target = schema(schemaName);
  if (!target.properties?.[propertyName]) {
    throw new Error(`missing ${propertyName} property on definition: ${schemaName}`);
  }
  const required = new Set(target.required || []);
  required.add(propertyName);
  target.required = Array.from(required).sort();
}

// Swagger 2 mirror of the bounds applied in openapi-postprocess.mjs so the
// runtime spec advertises the same array limits as the published OpenAPI.
function applyStringArrayPropertyBounds(schemaName, propertyName, maxItems, itemMaxLength) {
  const target = schema(schemaName);
  const property = target.properties?.[propertyName];
  if (!property || property.type !== "array") {
    throw new Error(`expected array property ${propertyName} on definition: ${schemaName}`);
  }
  property.maxItems = maxItems;
  property.items = property.items || {};
  property.items.maxLength = itemMaxLength;
}

function applyArrayMaxItems(operationPath, method, statusCode, maxItems) {
  const op = spec.paths?.[operationPath]?.[method];
  if (!op) {
    throw new Error(`missing operation: ${method.toUpperCase()} ${operationPath}`);
  }
  const response = op.responses?.[statusCode];
  // Swagger 2 carries the response schema directly under `schema`.
  const schemaRef = response?.schema;
  if (!schemaRef || schemaRef.type !== "array") {
    throw new Error(`expected array schema on ${method.toUpperCase()} ${operationPath} ${statusCode}`);
  }
  schemaRef.maxItems = maxItems;
}

function ensureAnthropicContentBlockSchema() {
  if (!spec.definitions) {
    throw new Error("missing Swagger definitions");
  }
  spec.definitions["anthropicapi.ContentBlock"] = {
    type: "object",
    properties: {
      content: anthropicContentSchema(),
      extra_content: freeFormObjectSchema(),
      id: { type: "string" },
      input: freeFormObjectSchema(),
      is_error: { type: "boolean" },
      name: { type: "string" },
      source: stringOrFreeFormObjectSchema(),
      text: { type: "string" },
      thinking: { type: "string" },
      // thinking blocks are replayed with the signature Anthropic issued;
      // redacted ones carry their opaque payload instead.
      signature: { type: "string" },
      data: { type: "string" },
      tool_use_id: { type: "string" },
      type: { type: "string" },
    },
  };
}

function applyResponsesReplayStateSchema() {
  schema("core.ResponsesOutputItem").properties.extra_content = freeFormObjectSchema();
}

function applyAnthropicMessageSchemas() {
  ensureAnthropicContentBlockSchema();
  schema("anthropicapi.Message").properties.content = anthropicContentSchema();
  schema("anthropicapi.MessagesRequest").properties.system = anthropicContentSchema();
  schema("anthropicapi.ResponseContentBlock").properties.input = freeFormObjectSchema();
  schema("anthropicapi.Tool").properties.input_schema = freeFormObjectSchema();
}

// Swagger 2 mirror of applyRetrieveModelDialects in openapi-postprocess.mjs:
// GET /v1/models/{model} answers a request carrying anthropic-version with the
// Anthropic model and error envelopes instead of the OpenAI ones.
function applyRetrieveModelDialects() {
  if (!spec.definitions) {
    throw new Error("missing Swagger definitions");
  }
  spec.definitions["anthropicapi.ModelInfo"] = {
    description: "One model in the Anthropic dialect, returned when the request carries anthropic-version.",
    type: "object",
    required: ["created_at", "display_name", "id", "type"],
    properties: {
      type: { type: "string", enum: ["model"] },
      id: { type: "string" },
      display_name: { type: "string" },
      created_at: { type: "string" },
    },
  };
  // A 401 is raised by the auth middleware before the handler runs, so it
  // stays in the OpenAI envelope whatever the header says.
  const dialects = {
    200: ["core.Model", "anthropicapi.ModelInfo"],
    404: ["core.OpenAIErrorEnvelope", "anthropicapi.ErrorResponse"],
    502: ["core.OpenAIErrorEnvelope", "anthropicapi.ErrorResponse"],
  };
  const responses = spec.paths?.["/v1/models/{model}"]?.get?.responses;
  if (!responses) {
    throw new Error("missing operation: GET /v1/models/{model}");
  }
  for (const [status, names] of Object.entries(dialects)) {
    const response = responses[status];
    if (!response?.schema) {
      throw new Error(`missing response schema ${status} on GET /v1/models/{model}`);
    }
    response.schema = {
      description: "OpenAI envelope by default; the Anthropic envelope when the request carries anthropic-version.",
      oneOf: names.map((name) => ({ $ref: `#/definitions/${name}` })),
    };
  }
}

applyAnthropicMessageSchemas();
applyResponsesReplayStateSchema();
applyRetrieveModelDialects();
ensureRequiredProperty("core.ResponsesConversationRef", "id");

// Virtual-models admin contract: mirror the required field and array bounds the
// OpenAPI postprocess applies, so the embedded Swagger 2 spec matches.
ensureRequiredProperty("admin.upsertVirtualModelRequest", "source");
ensureRequiredProperty("admin.deleteVirtualModelRequest", "source");
applyStringArrayPropertyBounds("admin.upsertVirtualModelRequest", "user_paths", 100, 1024);
applyArrayMaxItems("/admin/virtual-models", "get", "200", 10000);
for (const name of [
  "core.ResponsesRequest",
  "core.ResponseInputTokensRequest",
  "core.ResponseCompactRequest",
]) {
  applyResponseConversationOneOf(name);
}

let rendered = JSON.stringify(spec, null, 4);
rendered = rendered.replace(
  `"schemes": [\n        "${schemesMarker}"\n    ]`,
  `"schemes": {{ marshal .Schemes }}`,
).replace(/`/g, rawBacktickJoin);

fs.writeFileSync(file, `${source.slice(0, templateStart)}${rendered}${source.slice(end)}`);
