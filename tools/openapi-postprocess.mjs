import fs from "node:fs";

const file = process.argv[2] || "docs/openapi.json";
const spec = JSON.parse(fs.readFileSync(file, "utf8"));

function parseServers(value) {
  const urls = (value || "")
    .split(",")
    .map((url) => url.trim())
    .filter(Boolean);
  if (urls.length === 0) {
    throw new Error("DOCS_API_SERVERS must include at least one URL");
  }
  return urls.map((url) => ({
    url,
    description: isLocalServer(url) ? "Local GoModel" : "GoModel HTTPS deployment",
  }));
}

function isLocalServer(url) {
  return /(^https?:\/\/)?(localhost|127\.0\.0\.1)(:|\/|$)/.test(url);
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

spec.servers = parseServers(process.env.DOCS_API_SERVERS);
ensureResponsesInputElementSchema();

for (const name of [
  "core.ResponsesRequest",
  "core.ResponseInputTokensRequest",
  "core.ResponseCompactRequest",
]) {
  applyResponseInputOneOf(name);
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
