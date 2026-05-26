import asyncio
import os

from agents import Agent, Runner, set_default_openai_client, set_tracing_disabled
from openai import AsyncOpenAI


set_default_openai_client(
    AsyncOpenAI(
        base_url=os.getenv("OPENAI_BASE_URL", "http://localhost:8080/v1"),
        api_key=os.getenv("GOMODEL_MASTER_KEY", "change-me"),
    ),
    use_for_tracing=False,
)
set_tracing_disabled(True)


agent = Agent(
    name="Gateway assistant",
    instructions="Be concise.",
    model=os.getenv("OPENAI_MODEL", "gpt-5-mini"),
)


async def main() -> None:
    result = await Runner.run(agent, "Reply with exactly ok.")
    print(result.final_output)


if __name__ == "__main__":
    asyncio.run(main())
