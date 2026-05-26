import OpenAI from "openai";
import {
  Agent,
  run,
  setDefaultOpenAIClient,
  setOpenAIAPI,
  setTracingDisabled,
} from "@openai/agents";

setDefaultOpenAIClient(
  new OpenAI({
    baseURL: process.env.OPENAI_BASE_URL ?? "http://localhost:8080/v1",
    apiKey: process.env.GOMODEL_MASTER_KEY ?? "change-me",
  }),
);
setOpenAIAPI("responses");
setTracingDisabled(true);

const agent = new Agent({
  name: "Gateway assistant",
  instructions: "Be concise.",
  model: process.env.OPENAI_MODEL ?? "gpt-5-mini",
});

const result = await run(agent, "Reply with exactly ok.");
console.log(result.finalOutput);
