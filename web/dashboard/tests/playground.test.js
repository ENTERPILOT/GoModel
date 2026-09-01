import test from "node:test";
import assert from "node:assert/strict";

import {
  DEFAULT_MAX_TOKENS,
  ENDPOINTS,
  buildPlaygroundRequest,
  clampJsonPanelWidth,
  createStreamAccumulator,
  defaultUserPathForModel,
  extractResponseText,
  extractUsage,
  maxJsonPanelWidth,
  normalizeEndpoint,
  playgroundModelOptions,
  playgroundUserPathHeader,
  playgroundUserPathOptions,
  sendableMessages,
  streamErrorMessage,
} from "../src/pages/playground/playgroundLogic.js";

const conversation = [
  { id: 1, role: "system", content: "Be terse." },
  { id: 2, role: "user", content: "Hi" },
  { id: 3, role: "assistant", content: "   " },
  { id: 4, role: "assistant", content: "Hello!" },
  { id: 5, role: "user", content: "Bye" },
];

test("endpoints map to the gateway's public paths", () => {
  assert.deepEqual(
    ENDPOINTS.map((e) => [e.id, e.path]),
    [
      ["chat", "/v1/chat/completions"],
      ["responses", "/v1/responses"],
      ["messages", "/v1/messages"],
    ],
  );
  assert.equal(normalizeEndpoint("messages"), "messages");
  assert.equal(normalizeEndpoint("bogus"), "chat");
  assert.equal(normalizeEndpoint(""), "chat");
});

test("sendableMessages drops blank messages and normalizes roles", () => {
  assert.deepEqual(sendableMessages([{ role: "tool", content: "x" }, { role: "user", content: "" }]), [
    { role: "user", content: "x" },
  ]);
  assert.deepEqual(sendableMessages(null), []);
});

test("buildPlaygroundRequest shapes a chat completion", () => {
  const body = buildPlaygroundRequest("chat", {
    model: " gpt-4o ",
    messages: conversation,
    stream: true,
  });
  assert.deepEqual(body, {
    model: "gpt-4o",
    messages: [
      { role: "system", content: "Be terse." },
      { role: "user", content: "Hi" },
      { role: "assistant", content: "Hello!" },
      { role: "user", content: "Bye" },
    ],
    stream: true,
  });
  const plain = buildPlaygroundRequest("chat", { model: "m", messages: [], maxTokens: 50 });
  assert.deepEqual(plain, { model: "m", messages: [], max_tokens: 50 });
});

test("buildPlaygroundRequest moves system text into Responses instructions", () => {
  const body = buildPlaygroundRequest("responses", {
    model: "gpt-4o",
    messages: [...conversation, { role: "system", content: "Second rule." }],
  });
  assert.deepEqual(body, {
    model: "gpt-4o",
    instructions: "Be terse.\n\nSecond rule.",
    input: [
      { role: "user", content: "Hi" },
      { role: "assistant", content: "Hello!" },
      { role: "user", content: "Bye" },
    ],
  });
  assert.equal(
    "instructions" in buildPlaygroundRequest("responses", { model: "m", messages: [] }),
    false,
  );
});

test("buildPlaygroundRequest always sends max_tokens to the Messages API", () => {
  const body = buildPlaygroundRequest("messages", {
    model: "claude",
    messages: conversation,
    stream: true,
  });
  assert.deepEqual(body, {
    model: "claude",
    max_tokens: DEFAULT_MAX_TOKENS,
    system: "Be terse.",
    messages: [
      { role: "user", content: "Hi" },
      { role: "assistant", content: "Hello!" },
      { role: "user", content: "Bye" },
    ],
    stream: true,
  });
  assert.equal(buildPlaygroundRequest("messages", { model: "c", maxTokens: 8 }).max_tokens, 8);
  assert.equal(buildPlaygroundRequest("nope", { model: "c" }), null);
});

test("extractResponseText reads every endpoint's answer", () => {
  assert.equal(
    extractResponseText("chat", { choices: [{ message: { role: "assistant", content: "Hey" } }] }),
    "Hey",
  );
  assert.equal(
    extractResponseText("chat", {
      choices: [{ message: { content: [{ type: "text", text: "A" }, { type: "text", text: "B" }] } }],
    }),
    "AB",
  );
  assert.equal(extractResponseText("chat", { choices: [{ message: { content: null, refusal: "No" } }] }), "No");
  assert.equal(extractResponseText("responses", { output_text: "Done" }), "Done");
  assert.equal(
    extractResponseText("responses", {
      output: [
        { type: "reasoning", summary: [] },
        { type: "message", content: [{ type: "output_text", text: "Out" }] },
      ],
    }),
    "Out",
  );
  assert.equal(
    extractResponseText("messages", { content: [{ type: "text", text: "Hi " }, { type: "text", text: "there" }] }),
    "Hi there",
  );
  assert.equal(extractResponseText("chat", null), "");
  assert.equal(extractResponseText("other", { content: "x" }), "");
});

test("extractUsage and streamErrorMessage normalize provider shapes", () => {
  assert.deepEqual(extractUsage({ usage: { prompt_tokens: 3, completion_tokens: 5 } }), { input: 3, output: 5 });
  assert.deepEqual(extractUsage({ usage: { input_tokens: 7, output_tokens: 1 } }), { input: 7, output: 1 });
  assert.equal(extractUsage({ usage: { total: 1 } }), null);
  assert.equal(extractUsage(null), null);
  assert.equal(streamErrorMessage({ error: { message: "boom", type: "x" } }), "boom");
  assert.equal(streamErrorMessage({ type: "error", error: { type: "overloaded_error" } }), "overloaded_error");
  assert.equal(streamErrorMessage({ error: "plain" }), "plain");
  assert.equal(streamErrorMessage({ choices: [] }), "");
});

test("chat stream accumulator rebuilds a completion from chunks", () => {
  const acc = createStreamAccumulator("chat");
  assert.equal(
    acc.push({ id: "c1", created: 5, model: "gpt", choices: [{ index: 0, delta: { role: "assistant", content: "" } }] }),
    "",
  );
  assert.equal(acc.push({ id: "c1", choices: [{ index: 0, delta: { content: "Hel" } }] }), "Hel");
  assert.equal(acc.push({ id: "c1", choices: [{ index: 0, delta: { content: "lo" }, finish_reason: "stop" }] }), "lo");
  assert.equal(acc.push({ id: "c1", choices: [], usage: { prompt_tokens: 1, completion_tokens: 2 } }), "");
  assert.equal(acc.push("[DONE]"), "");
  assert.equal(acc.events, 4);
  assert.equal(acc.error, "");
  assert.deepEqual(acc.result(), {
    id: "c1",
    object: "chat.completion",
    created: 5,
    model: "gpt",
    choices: [{ index: 0, message: { role: "assistant", content: "Hello" }, finish_reason: "stop" }],
    usage: { prompt_tokens: 1, completion_tokens: 2 },
  });
});

test("responses stream accumulator keeps the completed response object", () => {
  const acc = createStreamAccumulator("responses");
  assert.equal(acc.push({ type: "response.created", response: { id: "r1", status: "in_progress" } }), "");
  assert.deepEqual(acc.result(), { id: "r1", status: "in_progress", output: [] });
  assert.equal(acc.push({ type: "response.output_text.delta", delta: "Hi" }), "Hi");
  assert.equal(acc.push({ type: "response.output_text.done", text: "Hi" }), "");
  const completed = {
    id: "r1",
    status: "completed",
    output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "Hi" }] }],
    usage: { input_tokens: 1, output_tokens: 1 },
  };
  assert.equal(acc.push({ type: "response.completed", response: completed }), "");
  assert.deepEqual(acc.result(), completed);
});

test("responses stream accumulator assembles output when the final event omits it", () => {
  const acc = createStreamAccumulator("responses");
  acc.push({ type: "response.created", response: { id: "r2", status: "in_progress" } });
  acc.push({
    type: "response.output_item.added",
    output_index: 0,
    item: { id: "msg_1", type: "message", role: "assistant", status: "in_progress", content: [] },
  });
  assert.equal(acc.push({ type: "response.output_text.delta", delta: "Hel" }), "Hel");
  assert.equal(acc.push({ type: "response.output_text.delta", delta: "lo" }), "lo");
  acc.push({
    type: "response.completed",
    response: { id: "r2", status: "completed", usage: { input_tokens: 3, output_tokens: 2 } },
  });
  assert.deepEqual(acc.result(), {
    id: "r2",
    status: "completed",
    usage: { input_tokens: 3, output_tokens: 2 },
    output: [
      {
        id: "msg_1",
        type: "message",
        role: "assistant",
        status: "in_progress",
        content: [{ type: "output_text", text: "Hello" }],
      },
    ],
  });
  assert.equal(extractResponseText("responses", acc.result()), "Hello");

  // Unindexed deltas keep appending to the latest assistant message even
  // after a function_call item was added behind it.
  const interleaved = createStreamAccumulator("responses");
  interleaved.push({
    type: "response.output_item.added",
    output_index: 0,
    item: { id: "msg_1", type: "message", role: "assistant", content: [] },
  });
  assert.equal(interleaved.push({ type: "response.output_text.delta", delta: "first " }), "first ");
  interleaved.push({
    type: "response.output_item.added",
    output_index: 1,
    item: { id: "fc_1", type: "function_call", name: "lookup", arguments: "{}" },
  });
  assert.equal(interleaved.push({ type: "response.output_text.delta", delta: "second" }), "second");
  assert.deepEqual(interleaved.result().output, [
    { id: "msg_1", type: "message", role: "assistant", content: [{ type: "output_text", text: "first second" }] },
    { id: "fc_1", type: "function_call", name: "lookup", arguments: "{}" },
  ]);

  // Deltas before any output item still land in a synthesized message.
  const bare = createStreamAccumulator("responses");
  bare.push({ type: "response.output_text.delta", delta: "x" });
  assert.deepEqual(bare.result(), {
    output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "x" }] }],
  });
});

test("messages stream accumulator assembles the Anthropic message", () => {
  const acc = createStreamAccumulator("messages");
  acc.push({
    type: "message_start",
    message: { id: "m1", role: "assistant", content: [], usage: { input_tokens: 4, output_tokens: 0 } },
  });
  acc.push({ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } });
  assert.equal(acc.push({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Hey" } }), "Hey");
  assert.equal(acc.push({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "!" } }), "!");
  acc.push({ type: "content_block_stop", index: 0 });
  acc.push({ type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: 2 } });
  acc.push({ type: "message_stop" });
  assert.deepEqual(acc.result(), {
    id: "m1",
    role: "assistant",
    content: [{ type: "text", text: "Hey!" }],
    usage: { input_tokens: 4, output_tokens: 2 },
    stop_reason: "end_turn",
  });
  assert.equal(acc.error, "");
});

test("stream accumulators surface in-band errors", () => {
  const acc = createStreamAccumulator("messages");
  acc.push({ type: "error", error: { type: "overloaded_error", message: "Overloaded" } });
  assert.equal(acc.error, "Overloaded");
  assert.equal(createStreamAccumulator("unknown").push({ any: 1 }), "");
});

test("playgroundModelOptions keeps enabled text models, deduplicated and sorted", () => {
  const options = playgroundModelOptions([
    { selector: "zeta", provider_name: "openai", model: { id: "zeta", metadata: { modes: ["chat"] } } },
    { selector: "embed", provider_name: "openai", model: { id: "embed", metadata: { modes: ["embedding"] } } },
    { selector: "alpha", provider_name: "anthropic", model: { id: "alpha", metadata: { modes: ["messages", "chat"] } } },
    { selector: "alpha", provider_name: "mirror", model: { id: "alpha" } },
    { selector: "off", provider_name: "x", model: { id: "off" }, access: { effective_enabled: false } },
    { provider_name: "ollama", model: { id: "llama3" } },
    { selector: "", model: { id: "" } },
  ]);
  assert.deepEqual(options, [
    { id: "alpha", label: "alpha", provider: "anthropic" },
    { id: "llama3", label: "llama3", provider: "ollama" },
    { id: "zeta", label: "zeta", provider: "openai" },
  ]);
  assert.deepEqual(playgroundModelOptions(undefined), []);
});

test("playgroundModelOptions strips a provider prefix that repeats the provider", () => {
  const options = playgroundModelOptions([
    { selector: "anthropic/claude-sonnet-4", provider_name: "anthropic", model: { id: "claude-sonnet-4" } },
    { selector: "Ollama/qwen2.5:0.5b", provider_name: "ollama", model: { id: "qwen2.5:0.5b" } },
    { selector: "openai-eu/gpt-4o", provider_name: "openai", model: { id: "gpt-4o" } },
    { selector: "anthropic/", provider_name: "anthropic", model: { id: "x" } },
  ]);
  assert.deepEqual(options, [
    { id: "anthropic/", label: "anthropic/", provider: "anthropic" },
    { id: "anthropic/claude-sonnet-4", label: "claude-sonnet-4", provider: "anthropic" },
    { id: "Ollama/qwen2.5:0.5b", label: "qwen2.5:0.5b", provider: "ollama" },
    { id: "openai-eu/gpt-4o", label: "openai-eu/gpt-4o", provider: "openai" },
  ]);
});

test("playgroundUserPathOptions returns the user_paths of the selected model as {value,label}", () => {
  const inventory = [
    { selector: "gpt-4o", provider_name: "openai", model: { id: "gpt-4o" } },
    {
      selector: "team-model",
      provider_name: "x",
      model: { id: "team-model" },
      access: { user_paths: ["/team/alpha", "/team/beta"] },
    },
  ];
  assert.deepEqual(playgroundUserPathOptions(inventory, "team-model"), [
    { value: "/team/alpha", label: "/team/alpha" },
    { value: "/team/beta", label: "/team/beta" },
  ]);
  // Whitespace around the selector still matches the trimmed id.
  assert.deepEqual(playgroundUserPathOptions(inventory, " team-model "), [
    { value: "/team/alpha", label: "/team/alpha" },
    { value: "/team/beta", label: "/team/beta" },
  ]);
  // Unrestricted, unknown, or malformed entries yield no options.
  assert.deepEqual(playgroundUserPathOptions(inventory, "gpt-4o"), []);
  assert.deepEqual(playgroundUserPathOptions(inventory, "nope"), []);
  assert.deepEqual(playgroundUserPathOptions(undefined, "team-model"), []);
  assert.deepEqual(
    playgroundUserPathOptions([{ selector: "x", access: { user_paths: "nope" } }], "x"),
    [],
  );
});

test("playgroundUserPathOptions skips non-string user_paths entries without throwing", () => {
  const inventory = [
    {
      selector: "team-model",
      access: { user_paths: ["/team/alpha", 42, null, "", { value: "/team/beta" }, "/team/beta"] },
    },
  ];
  assert.deepEqual(playgroundUserPathOptions(inventory, "team-model"), [
    { value: "/team/alpha", label: "/team/alpha" },
    { value: "/team/beta", label: "/team/beta" },
  ]);
});

test("defaultUserPathForModel returns the first allowed path or empty", () => {
  const inventory = [
    { selector: "gpt-4o", model: { id: "gpt-4o" }, access: { user_paths: [] } },
    {
      selector: "team-model",
      model: { id: "team-model" },
      access: { user_paths: ["/team/alpha", "/team/beta"] },
    },
  ];
  assert.equal(defaultUserPathForModel(inventory, "team-model"), "/team/alpha");
  assert.equal(defaultUserPathForModel(inventory, "gpt-4o"), "");
  assert.equal(defaultUserPathForModel(inventory, "nope"), "");
  assert.equal(defaultUserPathForModel(undefined, "team-model"), "");
});

test("playgroundUserPathHeader sends X-GoModel-User-Path only when non-empty", () => {
  assert.deepEqual(playgroundUserPathHeader("/team/alpha"), { "X-GoModel-User-Path": "/team/alpha" });
  // Surrounding whitespace is trimmed before the header is built.
  assert.deepEqual(playgroundUserPathHeader("  /team/alpha  "), { "X-GoModel-User-Path": "/team/alpha" });
  // Unrestricted selection or blank input drops the header.
  assert.deepEqual(playgroundUserPathHeader(""), {});
  assert.deepEqual(playgroundUserPathHeader("   "), {});
  assert.deepEqual(playgroundUserPathHeader(undefined), {});
});

test("clampJsonPanelWidth keeps the panel inside the viewport", () => {
  assert.equal(clampJsonPanelWidth(420, 1440), 420);
  assert.equal(clampJsonPanelWidth(100, 1440), 280);
  assert.equal(clampJsonPanelWidth(5000, 1440), 760);
  assert.equal(clampJsonPanelWidth(500, 600), 360);
  assert.equal(clampJsonPanelWidth("nope", 1440), 420);
  assert.equal(clampJsonPanelWidth(400, 0), 280);
  assert.equal(maxJsonPanelWidth(1440), 760);
  assert.equal(maxJsonPanelWidth(600), 360);
  assert.equal(maxJsonPanelWidth(0), 280);
});
