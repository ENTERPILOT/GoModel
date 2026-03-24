#!/usr/bin/env python3
import argparse
import csv
import json
import re
import shutil
from pathlib import Path

import matplotlib.pyplot as plt
import numpy as np


WORKLOADS = [
    {
        "key": "chat_nonstream",
        "label": "Chat non-stream",
        "endpoint": "chat",
        "mode": "nonstream",
        "gates": {
            "baseline": {"hey": "baseline_chat_nonstream_hey.txt"},
            "gomodel": {
                "hey": "gomodel_chat_nonstream_hey.txt",
                "resources": "gomodel_chat_nonstream_resources.csv",
            },
            "litellm": {
                "hey": "litellm_chat_nonstream_hey.txt",
                "resources": "litellm_chat_nonstream_resources.csv",
            },
        },
    },
    {
        "key": "chat_stream",
        "label": "Chat stream",
        "endpoint": "chat",
        "mode": "stream",
        "gates": {
            "baseline": {"stream": "baseline_chat_stream.json"},
            "gomodel": {
                "stream": "gomodel_chat_stream_stream.json",
                "resources": "gomodel_chat_stream_resources.csv",
            },
            "litellm": {
                "stream": "litellm_chat_stream_stream.json",
                "resources": "litellm_chat_stream_resources.csv",
            },
        },
    },
    {
        "key": "responses_nonstream",
        "label": "Responses non-stream",
        "endpoint": "responses",
        "mode": "nonstream",
        "gates": {
            "gomodel": {
                "hey": "gomodel_resp_nonstream_hey.txt",
                "resources": "gomodel_resp_nonstream_resources.csv",
            },
            "litellm": {
                "hey": "litellm_resp_nonstream_hey.txt",
                "resources": "litellm_resp_nonstream_resources.csv",
            },
        },
    },
    {
        "key": "responses_stream",
        "label": "Responses stream",
        "endpoint": "responses",
        "mode": "stream",
        "gates": {
            "gomodel": {
                "stream": "gomodel_resp_stream_stream.json",
                "resources": "gomodel_resp_stream_resources.csv",
            },
            "litellm": {
                "stream": "litellm_resp_stream_stream.json",
                "resources": "litellm_resp_stream_resources.csv",
            },
        },
    },
]

COLORS = {"gomodel": "#0E7490", "litellm": "#F97316", "baseline": "#64748B"}
BLOG_FILENAMES = {
    "dashboard": "gomodel-vs-litellm-march-2026-dashboard.png",
    "throughput": "gomodel-vs-litellm-march-2026-throughput.png",
    "latency": "gomodel-vs-litellm-march-2026-latency.png",
    "memory": "gomodel-vs-litellm-march-2026-memory.png",
    "speedup": "gomodel-vs-litellm-march-2026-speedup.png",
}


def parse_hey_output(raw: str) -> dict:
    requests_per_sec = _extract_float(raw, r"Requests/sec:\s+([0-9.]+)")
    average_secs = _extract_float(raw, r"Average:\s+([0-9.]+)\s+secs")
    latency_ms = {"avg": round(average_secs * 1000.0, 4)}
    for percentile in ("50", "95", "99"):
        secs = _extract_float(raw, rf"{percentile}%% in ([0-9.]+) secs")
        latency_ms[f"p{percentile}"] = round(secs * 1000.0, 4)
    return {"requests_per_sec": requests_per_sec, "latency_ms": latency_ms}


def normalize_streaming_result(raw: dict) -> dict:
    return {
        "requests_per_sec": float(raw["rps"]),
        "avg_chunks": int(raw["avg_chunks"]),
        "ttfb_ms": _latency_block_to_ms(raw["ttfb"]),
        "total_latency_ms": _latency_block_to_ms(raw["total_latency"]),
    }


def parse_resources_csv(path: Path) -> dict:
    if not path.exists():
        return {}
    rss_values = []
    cpu_values = []
    with path.open("r", encoding="utf-8", newline="") as handle:
        reader = csv.DictReader(handle)
        for row in reader:
            rss_kb = float(row["rss_kb"])
            cpu_pct = float(row["cpu_pct"])
            if rss_kb > 0:
                rss_values.append(rss_kb)
            cpu_values.append(cpu_pct)
    if not rss_values:
        return {}
    return {
        "peak_rss_mb": round(max(rss_values) / 1024.0, 1),
        "avg_rss_mb": round(sum(rss_values) / len(rss_values) / 1024.0, 1),
        "avg_cpu_pct": round(sum(cpu_values) / len(cpu_values), 1) if cpu_values else 0.0,
    }


def load_results(results_dir: Path) -> dict:
    normalized = {}
    for workload in WORKLOADS:
        rows = {}
        for gateway, files in workload["gates"].items():
            row = {
                "gateway": gateway,
                "workload_key": workload["key"],
                "workload_label": workload["label"],
                "endpoint": workload["endpoint"],
                "mode": workload["mode"],
            }
            if "hey" in files:
                raw = (results_dir / files["hey"]).read_text(encoding="utf-8")
                row.update(parse_hey_output(raw))
            if "stream" in files:
                raw = json.loads((results_dir / files["stream"]).read_text(encoding="utf-8"))
                row.update(normalize_streaming_result(raw))
            if "resources" in files:
                row["resources"] = parse_resources_csv(results_dir / files["resources"])
            rows[gateway] = row
        normalized[workload["key"]] = rows
    return normalized


def build_summary(dataset: dict) -> dict:
    comparisons = {}
    for workload_key, rows in dataset.items():
        gomodel = rows.get("gomodel")
        litellm = rows.get("litellm")
        baseline = rows.get("baseline")
        if gomodel and litellm:
            if gomodel["mode"] == "nonstream":
                gomodel_latency = gomodel["latency_ms"]["p50"]
                litellm_latency = litellm["latency_ms"]["p50"]
            else:
                gomodel_latency = gomodel["ttfb_ms"]["p50"]
                litellm_latency = litellm["ttfb_ms"]["p50"]
            comparisons[workload_key] = {
                "throughput_speedup_vs_litellm": round(
                    gomodel["requests_per_sec"] / litellm["requests_per_sec"], 2
                ),
                "latency_advantage_vs_litellm": round(litellm_latency / gomodel_latency, 2),
                "memory_advantage_vs_litellm": round(
                    litellm["resources"]["peak_rss_mb"] / gomodel["resources"]["peak_rss_mb"], 2
                ),
            }
        if baseline and "latency_ms" in baseline and gomodel:
            comparisons.setdefault(workload_key, {})
            comparisons[workload_key]["gomodel_added_latency_vs_baseline_ms"] = round(
                gomodel["latency_ms"]["p50"] - baseline["latency_ms"]["p50"], 2
            )
            comparisons[workload_key]["litellm_added_latency_vs_baseline_ms"] = round(
                litellm["latency_ms"]["p50"] - baseline["latency_ms"]["p50"], 2
            )
        if baseline and "ttfb_ms" in baseline and gomodel:
            comparisons.setdefault(workload_key, {})
            comparisons[workload_key]["gomodel_added_ttfb_vs_baseline_ms"] = round(
                gomodel["ttfb_ms"]["p50"] - baseline["ttfb_ms"]["p50"], 2
            )
            comparisons[workload_key]["litellm_added_ttfb_vs_baseline_ms"] = round(
                litellm["ttfb_ms"]["p50"] - baseline["ttfb_ms"]["p50"], 2
            )

    return {"results": dataset, "comparisons": comparisons}


def render_charts(summary: dict, output_dir: Path) -> dict:
    charts_dir = output_dir / "charts"
    charts_dir.mkdir(parents=True, exist_ok=True)
    plt.style.use("seaborn-v0_8-whitegrid")

    throughput_path = charts_dir / BLOG_FILENAMES["throughput"]
    latency_path = charts_dir / BLOG_FILENAMES["latency"]
    memory_path = charts_dir / BLOG_FILENAMES["memory"]
    speedup_path = charts_dir / BLOG_FILENAMES["speedup"]
    dashboard_path = charts_dir / BLOG_FILENAMES["dashboard"]

    _plot_grouped_metric(
        summary["results"],
        throughput_path,
        title="Throughput by workload",
        value_getter=lambda row: row["requests_per_sec"],
        ylabel="Requests / second",
    )
    _plot_grouped_metric(
        summary["results"],
        latency_path,
        title="Median latency by workload",
        value_getter=lambda row: row["latency_ms"]["p50"] if row["mode"] == "nonstream" else row["ttfb_ms"]["p50"],
        ylabel="Milliseconds",
        subtitle="Non-stream uses p50 latency. Stream uses p50 TTFB.",
    )
    _plot_grouped_metric(
        summary["results"],
        memory_path,
        title="Peak RSS by workload",
        value_getter=lambda row: row["resources"]["peak_rss_mb"],
        ylabel="MB",
    )
    _plot_speedup(summary["comparisons"], speedup_path)
    _plot_dashboard(summary, dashboard_path)

    return {
        "dashboard": str(dashboard_path),
        "throughput": str(throughput_path),
        "latency": str(latency_path),
        "memory": str(memory_path),
        "speedup": str(speedup_path),
    }


def copy_blog_charts(charts: dict, blog_public_dir: Path) -> None:
    blog_public_dir.mkdir(parents=True, exist_ok=True)
    for chart_path in charts.values():
        src = Path(chart_path)
        shutil.copy2(src, blog_public_dir / src.name)


def _extract_float(raw: str, pattern: str) -> float:
    match = re.search(pattern, raw)
    if not match:
        raise ValueError(f"pattern not found: {pattern}")
    return float(match.group(1))


def _latency_block_to_ms(block: dict) -> dict:
    return {
        "p50": round(float(block["p50_us"]) / 1000.0, 5),
        "p95": round(float(block["p95_us"]) / 1000.0, 5),
        "p99": round(float(block["p99_us"]) / 1000.0, 5),
    }


def _plot_grouped_metric(results: dict, output_path: Path, title: str, value_getter, ylabel: str, subtitle: str | None = None) -> None:
    categories = []
    gomodel_values = []
    litellm_values = []
    baseline_values = []
    baseline_present = False
    for workload_key in [item["key"] for item in WORKLOADS]:
        rows = results[workload_key]
        categories.append(rows["gomodel"]["workload_label"] if "gomodel" in rows else rows["litellm"]["workload_label"])
        gomodel_values.append(value_getter(rows["gomodel"]))
        litellm_values.append(value_getter(rows["litellm"]))
        if "baseline" in rows:
            try:
                baseline_value = value_getter(rows["baseline"])
            except KeyError:
                baseline_value = np.nan
            baseline_values.append(baseline_value)
            if not np.isnan(baseline_value):
                baseline_present = True
        else:
            baseline_values.append(np.nan)

    x = np.arange(len(categories))
    width = 0.24 if baseline_present else 0.32
    fig, ax = plt.subplots(figsize=(12, 6.4), constrained_layout=True)
    if baseline_present:
        baseline_bars = ax.bar(x - width, baseline_values, width, label="Direct baseline", color=COLORS["baseline"])
        _label_bars(ax, baseline_bars)
    gomodel_bars = ax.bar(x, gomodel_values, width, label="GoModel", color=COLORS["gomodel"])
    litellm_bars = ax.bar(x + width, litellm_values, width, label="LiteLLM", color=COLORS["litellm"])
    _label_bars(ax, gomodel_bars)
    _label_bars(ax, litellm_bars)

    ax.set_title(title, fontsize=16, weight="bold")
    if subtitle:
        ax.text(0.0, 1.02, subtitle, transform=ax.transAxes, fontsize=10, color="#475569")
    ax.set_ylabel(ylabel)
    ax.set_xticks(x, categories)
    ax.legend(frameon=True)
    ax.grid(axis="y", alpha=0.25)
    fig.savefig(output_path, dpi=180)
    plt.close(fig)


def _plot_speedup(comparisons: dict, output_path: Path) -> None:
    categories = [item["label"] for item in WORKLOADS]
    throughput = [comparisons[item["key"]]["throughput_speedup_vs_litellm"] for item in WORKLOADS]
    latency = [comparisons[item["key"]]["latency_advantage_vs_litellm"] for item in WORKLOADS]

    x = np.arange(len(categories))
    width = 0.34
    fig, ax = plt.subplots(figsize=(12, 6.4), constrained_layout=True)
    bars_a = ax.bar(x - width / 2, throughput, width, label="Throughput speedup", color="#0891B2")
    bars_b = ax.bar(x + width / 2, latency, width, label="Lower-latency factor", color="#14B8A6")
    _label_bars(ax, bars_a, suffix="x")
    _label_bars(ax, bars_b, suffix="x")

    ax.axhline(1.0, color="#94A3B8", linewidth=1, linestyle="--")
    ax.set_title("GoModel advantage vs LiteLLM", fontsize=16, weight="bold")
    ax.set_ylabel("Factor")
    ax.set_xticks(x, categories)
    ax.legend(frameon=True)
    ax.grid(axis="y", alpha=0.25)
    fig.savefig(output_path, dpi=180)
    plt.close(fig)


def _plot_dashboard(summary: dict, output_path: Path) -> None:
    categories = [item["label"] for item in WORKLOADS]
    throughput = [summary["results"][item["key"]]["gomodel"]["requests_per_sec"] for item in WORKLOADS]
    throughput_lite = [summary["results"][item["key"]]["litellm"]["requests_per_sec"] for item in WORKLOADS]
    latency = [
        summary["results"][item["key"]]["gomodel"]["latency_ms"]["p50"]
        if item["mode"] == "nonstream"
        else summary["results"][item["key"]]["gomodel"]["ttfb_ms"]["p50"]
        for item in WORKLOADS
    ]
    latency_lite = [
        summary["results"][item["key"]]["litellm"]["latency_ms"]["p50"]
        if item["mode"] == "nonstream"
        else summary["results"][item["key"]]["litellm"]["ttfb_ms"]["p50"]
        for item in WORKLOADS
    ]
    memory = [summary["results"][item["key"]]["gomodel"]["resources"]["peak_rss_mb"] for item in WORKLOADS]
    memory_lite = [summary["results"][item["key"]]["litellm"]["resources"]["peak_rss_mb"] for item in WORKLOADS]
    speedup = [summary["comparisons"][item["key"]]["throughput_speedup_vs_litellm"] for item in WORKLOADS]

    x = np.arange(len(categories))
    width = 0.36
    fig, axes = plt.subplots(2, 2, figsize=(15, 10), constrained_layout=True)
    fig.suptitle("GoModel vs LiteLLM: March 23, 2026 localhost benchmark", fontsize=18, weight="bold")

    axes[0, 0].bar(x - width / 2, throughput, width, label="GoModel", color=COLORS["gomodel"])
    axes[0, 0].bar(x + width / 2, throughput_lite, width, label="LiteLLM", color=COLORS["litellm"])
    axes[0, 0].set_title("Throughput")
    axes[0, 0].set_xticks(x, categories)
    axes[0, 0].set_ylabel("Req/s")
    axes[0, 0].legend(frameon=True)

    axes[0, 1].bar(x - width / 2, latency, width, label="GoModel", color=COLORS["gomodel"])
    axes[0, 1].bar(x + width / 2, latency_lite, width, label="LiteLLM", color=COLORS["litellm"])
    axes[0, 1].set_title("Median latency / TTFB")
    axes[0, 1].set_xticks(x, categories)
    axes[0, 1].set_ylabel("ms")

    axes[1, 0].bar(x - width / 2, memory, width, label="GoModel", color=COLORS["gomodel"])
    axes[1, 0].bar(x + width / 2, memory_lite, width, label="LiteLLM", color=COLORS["litellm"])
    axes[1, 0].set_title("Peak RSS")
    axes[1, 0].set_xticks(x, categories)
    axes[1, 0].set_ylabel("MB")

    speedup_bars = axes[1, 1].bar(x, speedup, width=0.5, color="#14B8A6")
    _label_bars(axes[1, 1], speedup_bars, suffix="x")
    axes[1, 1].axhline(1.0, color="#94A3B8", linewidth=1, linestyle="--")
    axes[1, 1].set_title("Throughput speedup vs LiteLLM")
    axes[1, 1].set_xticks(x, categories)
    axes[1, 1].set_ylabel("factor")

    for ax in axes.flat:
        ax.grid(axis="y", alpha=0.25)

    fig.savefig(output_path, dpi=180)
    plt.close(fig)


def _label_bars(ax, bars, suffix: str = "") -> None:
    for bar in bars:
        height = bar.get_height()
        if np.isnan(height):
            continue
        label = f"{height:.1f}{suffix}" if height < 100 else f"{height:,.0f}{suffix}"
        ax.annotate(
            label,
            (bar.get_x() + bar.get_width() / 2, height),
            textcoords="offset points",
            xytext=(0, 4),
            ha="center",
            fontsize=8,
        )


def main() -> None:
    parser = argparse.ArgumentParser(description="Normalize the March 23 benchmark artifacts and generate blog charts.")
    parser.add_argument(
        "--results-dir",
        type=Path,
        required=True,
        help="Path to docs/2026-03-23_benchmark_scripts/gateway-comparison/results",
    )
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=Path(__file__).resolve().parent / "output",
        help="Directory for normalized JSON and generated charts",
    )
    parser.add_argument(
        "--blog-public-dir",
        type=Path,
        help="Optional blog public charts directory to copy generated chart images into",
    )
    args = parser.parse_args()

    results_dir = args.results_dir.resolve()
    output_dir = args.output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    dataset = load_results(results_dir)
    summary = build_summary(dataset)
    summary["source_results_dir"] = str(results_dir)
    summary_path = output_dir / "benchmark_summary.json"
    summary_path.write_text(json.dumps(summary, indent=2), encoding="utf-8")

    charts = render_charts(summary, output_dir)
    if args.blog_public_dir:
        copy_blog_charts(charts, args.blog_public_dir.resolve())

    print(f"Wrote normalized summary to {summary_path}")
    print(f"Generated charts in {output_dir / 'charts'}")
    if args.blog_public_dir:
        print(f"Copied blog chart assets to {args.blog_public_dir.resolve()}")


if __name__ == "__main__":
    main()
