import asyncio
import json
import os
from collections.abc import Awaitable, Callable

from openai import AsyncOpenAI


BASE_URL = os.getenv("OPENAI_BASE_URL", "http://localhost:8080/v1")
API_KEY = os.getenv("GOMODEL_MASTER_KEY") or os.getenv("OPENAI_API_KEY", "change-me")
MODEL = (
    os.getenv("ANTHROPIC_MODEL")
    or os.getenv("OPENAI_MODEL")
    or "anthropic/claude-sonnet-4-20250514"
)


async def run_case(
    name: str,
    call: Callable[[], Awaitable[object]],
    *,
    expect_error_contains: str | None = None,
) -> bool:
    try:
        result = await call()
    except Exception as exc:
        message = str(exc)
        if expect_error_contains and expect_error_contains in message:
            print(f"PASS {name}: expected unsupported path ({expect_error_contains})")
            return True
        print(f"FAIL {name}: unexpected error: {message}")
        return False

    if expect_error_contains:
        print(f"FAIL {name}: expected error containing {expect_error_contains!r}")
        return False

    output_text = response_summary(result)
    print(f"PASS {name}: {output_text[:160].replace(chr(10), ' ')}")
    return True


def response_summary(result: object) -> str:
    try:
        output_text = getattr(result, "output_text", "") or ""
    except TypeError:
        output_text = ""
    if output_text:
        return output_text

    output = getattr(result, "output", None)
    if output:
        parts = []
        for item in output:
            item_type = getattr(item, "type", None) or "unknown"
            name = getattr(item, "name", None)
            parts.append(f"{item_type}:{name}" if name else str(item_type))
        return ", ".join(parts)

    return type(result).__name__


async def main() -> int:
    client = AsyncOpenAI(base_url=BASE_URL, api_key=API_KEY)
    cases: list[tuple[str, Callable[[], Awaitable[object]], str | None]] = [
        (
            "plain responses call",
            lambda: client.responses.create(
                model=MODEL,
                instructions="Be concise. Do not mention implementation details.",
                input=(
                    "Give three short bullets for what an AI gateway should verify "
                    "before routing an agent request."
                ),
            ),
            None,
        ),
        (
            "forced function tool call",
            lambda: client.responses.create(
                model=MODEL,
                input="Use the tool to inspect order A123 and do not answer directly.",
                tools=[
                    {
                        "type": "function",
                        "name": "lookup_order",
                        "description": "Look up an order by id.",
                        "parameters": {
                            "type": "object",
                            "properties": {
                                "order_id": {
                                    "type": "string",
                                    "description": "The customer order id.",
                                }
                            },
                            "required": ["order_id"],
                            "additionalProperties": False,
                        },
                    }
                ],
                tool_choice={"type": "function", "name": "lookup_order"},
            ),
            None,
        ),
        (
            "json schema structured output gap",
            lambda: client.responses.create(
                model=MODEL,
                input="Return a JSON object with ok=true and one detected_gap string.",
                text={
                    "format": {
                        "type": "json_schema",
                        "name": "probe_result",
                        "strict": True,
                        "schema": {
                            "type": "object",
                            "properties": {
                                "ok": {"type": "boolean"},
                                "detected_gap": {"type": "string"},
                            },
                            "required": ["ok", "detected_gap"],
                            "additionalProperties": False,
                        },
                    }
                },
            ),
            "response_format",
        ),
        (
            "previous_response_id state gap",
            lambda: client.responses.create(
                model=MODEL,
                input="Continue from previous state.",
                previous_response_id="resp_probe_previous",
            ),
            "previous_response_id",
        ),
        (
            "unknown input item gap",
            lambda: client.responses.create(
                model=MODEL,
                input=[
                    {
                        "type": "reasoning",
                        "id": "rs_probe",
                        "summary": [
                            {"type": "summary_text", "text": "Synthetic prior reasoning."}
                        ],
                    },
                    {"role": "user", "content": "Continue."},
                ],
            ),
            "unsupported input item type",
        ),
        (
            "hosted web search gap",
            lambda: client.responses.create(
                model=MODEL,
                input="Search the web for the latest Go release.",
                tools=[{"type": "web_search_preview"}],
            ),
            "web_search_preview",
        ),
        (
            "hosted file search gap",
            lambda: client.responses.create(
                model=MODEL,
                input="Search the attached vector store.",
                tools=[{"type": "file_search", "vector_store_ids": ["vs_probe"]}],
            ),
            "file_search",
        ),
        (
            "hosted computer use gap",
            lambda: client.responses.create(
                model=MODEL,
                input="Use the computer to inspect the page.",
                tools=[
                    {
                        "type": "computer_use_preview",
                        "display_width": 1024,
                        "display_height": 768,
                        "environment": "browser",
                    }
                ],
            ),
            "computer_use_preview",
        ),
    ]

    results = []
    for name, call, expected_error in cases:
        results.append(
            await run_case(name, call, expect_error_contains=expected_error)
        )

    passed = sum(1 for result in results if result)
    print(json.dumps({"passed": passed, "total": len(results), "model": MODEL}, indent=2))
    return 0 if all(results) else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
