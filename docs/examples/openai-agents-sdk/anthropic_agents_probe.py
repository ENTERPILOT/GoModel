import asyncio
import json
import os
from collections.abc import Awaitable, Callable

from openai import AsyncOpenAI

try:
    from agents import (
        Agent,
        MultiProvider,
        RunConfig,
        Runner,
        function_tool,
        set_default_openai_client,
        set_tracing_disabled,
    )
except ImportError as exc:
    raise SystemExit(
        "Missing dependency: install with `pip install openai-agents openai`."
    ) from exc


BASE_URL = os.getenv("OPENAI_BASE_URL", "http://localhost:8080/v1")
API_KEY = os.getenv("GOMODEL_MASTER_KEY") or os.getenv("OPENAI_API_KEY", "change-me")
CLIENT = AsyncOpenAI(
    base_url=BASE_URL,
    api_key=API_KEY,
)
MODEL = (
    os.getenv("ANTHROPIC_MODEL")
    or os.getenv("OPENAI_MODEL")
    or "anthropic/claude-sonnet-4-20250514"
)


set_default_openai_client(
    CLIENT,
    use_for_tracing=False,
)
set_tracing_disabled(True)

RUN_CONFIG = RunConfig(
    model_provider=MultiProvider(
        openai_client=CLIENT,
        unknown_prefix_mode="model_id",
        openai_prefix_mode="model_id",
    )
)


@function_tool
def lookup_inventory(sku: str) -> str:
    """Look up inventory availability for a SKU."""
    if sku == "WIDGET-42":
        return json.dumps({"sku": sku, "status": "in_stock", "quantity": 17})
    return json.dumps({"sku": sku, "status": "unknown", "quantity": 0})


async def run_case(
    name: str,
    call: Callable[[], Awaitable[str]],
    *,
    must_contain: str,
) -> bool:
    try:
        output = await call()
    except Exception as exc:
        print(f"FAIL {name}: unexpected error: {exc}")
        return False

    normalized = output.lower()
    if must_contain.lower() not in normalized:
        print(f"FAIL {name}: output did not contain {must_contain!r}: {output[:240]}")
        return False

    print(f"PASS {name}: {output[:160].replace(chr(10), ' ')}")
    return True


async def run_basic() -> str:
    agent = Agent(
        name="Anthropic gateway probe",
        instructions="Be exact and concise.",
        model=MODEL,
    )
    result = await Runner.run(
        agent,
        "Reply with exactly: gateway-ok",
        run_config=RUN_CONFIG,
    )
    return str(result.final_output)


async def run_tool_loop() -> str:
    agent = Agent(
        name="Anthropic gateway tool probe",
        instructions=(
            "You must call lookup_inventory before answering. "
            "Include the SKU and status in your final answer."
        ),
        model=MODEL,
        tools=[lookup_inventory],
    )
    result = await Runner.run(
        agent,
        "Check inventory for SKU WIDGET-42.",
        run_config=RUN_CONFIG,
    )
    return str(result.final_output)


async def run_streamed_tool_loop() -> str:
    agent = Agent(
        name="Anthropic gateway streaming tool probe",
        instructions=(
            "You must call lookup_inventory before answering. "
            "Include the quantity in your final answer."
        ),
        model=MODEL,
        tools=[lookup_inventory],
    )
    result = Runner.run_streamed(
        agent,
        "Check inventory for SKU WIDGET-42.",
        run_config=RUN_CONFIG,
    )
    async for _event in result.stream_events():
        pass
    return str(result.final_output)


async def main() -> int:
    cases: list[tuple[str, Callable[[], Awaitable[str]], str]] = [
        ("basic agents run", run_basic, "gateway-ok"),
        ("function tool loop", run_tool_loop, "17"),
        ("streamed function tool loop", run_streamed_tool_loop, "17"),
    ]

    results = []
    for name, call, must_contain in cases:
        results.append(await run_case(name, call, must_contain=must_contain))

    passed = sum(1 for result in results if result)
    print(json.dumps({"passed": passed, "total": len(results), "model": MODEL}, indent=2))
    return 0 if all(results) else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
