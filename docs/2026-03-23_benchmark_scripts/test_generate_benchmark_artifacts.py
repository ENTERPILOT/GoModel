import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from generate_benchmark_artifacts import normalize_streaming_result, parse_hey_output


HEY_SAMPLE = """
Summary:
  Total:\t0.0414 secs
  Slowest:\t0.0084 secs
  Fastest:\t0.0002 secs
  Average:\t0.0020 secs
  Requests/sec:\t24128.7268

Latency distribution:
  10%% in 0.0009 secs
  25%% in 0.0015 secs
  50%% in 0.0019 secs
  75%% in 0.0022 secs
  90%% in 0.0027 secs
  95%% in 0.0039 secs
  99%% in 0.0079 secs
"""

STREAMING_SAMPLE = {
    "avg_chunks": 34,
    "rps": 3929.187537806888,
    "ttfb": {
        "p50_us": 12127,
        "p95_us": 14262.949999999999,
        "p99_us": 17257.29,
    },
    "total_latency": {
        "p50_us": 12662,
        "p95_us": 14870.1,
        "p99_us": 17440.979999999996,
    },
}


class ParseHeyOutputTests(unittest.TestCase):
    def test_parses_numeric_latency_percentiles_from_hey_output(self) -> None:
        metrics = parse_hey_output(HEY_SAMPLE)

        self.assertAlmostEqual(metrics["requests_per_sec"], 24128.7268)
        self.assertAlmostEqual(metrics["latency_ms"]["avg"], 2.0)
        self.assertAlmostEqual(metrics["latency_ms"]["p50"], 1.9)
        self.assertAlmostEqual(metrics["latency_ms"]["p95"], 3.9)
        self.assertAlmostEqual(metrics["latency_ms"]["p99"], 7.9)


class NormalizeStreamingResultTests(unittest.TestCase):
    def test_converts_streaming_microseconds_to_milliseconds(self) -> None:
        metrics = normalize_streaming_result(STREAMING_SAMPLE)

        self.assertAlmostEqual(metrics["requests_per_sec"], 3929.187537806888)
        self.assertAlmostEqual(metrics["ttfb_ms"]["p50"], 12.127)
        self.assertAlmostEqual(metrics["ttfb_ms"]["p95"], 14.26295)
        self.assertAlmostEqual(metrics["total_latency_ms"]["p99"], 17.44098)
        self.assertEqual(metrics["avg_chunks"], 34)


if __name__ == "__main__":
    unittest.main()
