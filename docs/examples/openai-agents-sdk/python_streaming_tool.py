import asyncio
import os

from agents import (
    Agent,
    Runner,
    function_tool,
    set_default_openai_client,
    set_tracing_disabled,
)
from openai import AsyncOpenAI


set_default_openai_client(
    AsyncOpenAI(
        base_url=os.getenv("OPENAI_BASE_URL", "http://localhost:8080/v1"),
        api_key=os.getenv("GOMODEL_MASTER_KEY", "change-me"),
    ),
    use_for_tracing=False,
)
set_tracing_disabled(True)


@function_tool
def gateway_status() -> str:
    """Return the status of the local gateway smoke test."""
    return "GoModel is reachable through the OpenAI-compatible Responses API."


agent = Agent(
    name="Gateway tool assistant",
    instructions="Use gateway_status when it helps. Be concise.",
    model=os.getenv("OPENAI_MODEL", "gpt-5-mini"),
    tools=[gateway_status],
)


async def main() -> None:
    result = Runner.run_streamed(
        agent,
        "Call the status tool, then summarize the result in one sentence.",
    )
    async for _event in result.stream_events():
        pass
    print(result.final_output)


if __name__ == "__main__":
    asyncio.run(main())
