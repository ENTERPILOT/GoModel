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

function ensureAnthropicContentBlockSchema() {
  if (!spec.definitions) {
    throw new Error("missing Swagger definitions");
  }
  spec.definitions["anthropicapi.ContentBlock"] = {
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

applyAnthropicMessageSchemas();
ensureRequiredProperty("core.ResponsesConversationRef", "id");
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
