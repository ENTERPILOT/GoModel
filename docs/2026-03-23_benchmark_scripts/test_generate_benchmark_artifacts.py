import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from generate_benchmark_artifacts import normalize_streaming_result, parse_hey_output


FIXTURES_DIR = Path(__file__).resolve().parent / "gateway-comparison" / "results"


class ParseHeyOutputTests(unittest.TestCase):
    def test_parses_numeric_latency_percentiles_from_hey_output(self) -> None:
        raw = (FIXTURES_DIR / "gomodel_chat_nonstream_hey.txt").read_text(encoding="utf-8")

        metrics = parse_hey_output(raw)

        self.assertAlmostEqual(metrics["requests_per_sec"], 24128.7268)
        self.assertAlmostEqual(metrics["latency_ms"]["avg"], 2.0)
        self.assertAlmostEqual(metrics["latency_ms"]["p50"], 1.9)
        self.assertAlmostEqual(metrics["latency_ms"]["p95"], 3.9)
        self.assertAlmostEqual(metrics["latency_ms"]["p99"], 7.9)


class NormalizeStreamingResultTests(unittest.TestCase):
    def test_converts_streaming_microseconds_to_milliseconds(self) -> None:
        raw = json.loads((FIXTURES_DIR / "gomodel_chat_stream_stream.json").read_text(encoding="utf-8"))

        metrics = normalize_streaming_result(raw)

        self.assertAlmostEqual(metrics["requests_per_sec"], 3929.187537806888)
        self.assertAlmostEqual(metrics["ttfb_ms"]["p50"], 12.127)
        self.assertAlmostEqual(metrics["ttfb_ms"]["p95"], 14.26295)
        self.assertAlmostEqual(metrics["total_latency_ms"]["p99"], 17.44098)
        self.assertEqual(metrics["avg_chunks"], 34)


if __name__ == "__main__":
    unittest.main()
