#!/usr/bin/env python3
import argparse
import json
from pathlib import Path

import matplotlib.pyplot as plt
import numpy as np


def load_rows(results_dir: Path):
    rows = []
    for path in sorted(results_dir.glob("*_c*.json")):
        with path.open("r", encoding="utf-8") as f:
            data = json.load(f)
        rows.append(
            {
                "gateway": data["gateway"],
                "c": int(data["concurrency"]),
                "req_per_sec": float(data["req_per_sec"]),
                "p95_ms": float(data["latency_ms"]["p95"]),
                "rss_mb": float(data["process"]["rss_avg_mb"]),
                "cpu_pct": float(data["process"]["cpu_avg"]),
                "error_rate": float(data["error_rate"]) * 100.0,
            }
        )
    return rows


def split_by_gateway(rows):
    grouped = {}
    for row in rows:
        grouped.setdefault(row["gateway"], []).append(row)
    for key in grouped:
        grouped[key] = sorted(grouped[key], key=lambda r: r["c"])
    return grouped


def extract_xy(rows, key):
    x = np.array([r["c"] for r in rows], dtype=int)
    y = np.array([r[key] for r in rows], dtype=float)
    return x, y


def plot_metric(ax, grouped, key, title, ylabel, colors):
    for gateway, rows in grouped.items():
        x, y = extract_xy(rows, key)
        ax.plot(
            x,
            y,
            marker="o",
            linewidth=2.5,
            markersize=7,
            label=gateway.upper(),
            color=colors.get(gateway, None),
        )
        for xi, yi in zip(x, y):
            ax.annotate(f"{yi:.1f}", (xi, yi), textcoords="offset points", xytext=(0, 7), ha="center", fontsize=8)
    ax.set_title(title, fontsize=12, weight="bold")
    ax.set_xlabel("Concurrency")
    ax.set_ylabel(ylabel)
    ax.set_xticks(sorted({r["c"] for rows in grouped.values() for r in rows}))
    ax.grid(alpha=0.25)
    ax.legend(frameon=True)


def save_single_plot(results_dir: Path, grouped, key, title, ylabel, filename, colors):
    fig, ax = plt.subplots(figsize=(8.5, 4.8), constrained_layout=True)
    plot_metric(ax, grouped, key, title, ylabel, colors)
    fig.savefig(results_dir / "charts" / filename, dpi=180)
    plt.close(fig)


def render_dashboard(results_dir: Path, grouped, colors):
    fig, axes = plt.subplots(2, 2, figsize=(14, 9), constrained_layout=True)
    fig.suptitle("GoModel vs LiteLLM Benchmark Dashboard", fontsize=18, weight="bold")

    plot_metric(axes[0, 0], grouped, "req_per_sec", "Throughput", "Req/s", colors)
    plot_metric(axes[0, 1], grouped, "p95_ms", "Latency (p95)", "Milliseconds", colors)
    plot_metric(axes[1, 0], grouped, "rss_mb", "Memory Footprint", "RSS Avg (MB)", colors)
    plot_metric(axes[1, 1], grouped, "cpu_pct", "CPU Usage", "CPU Avg (%)", colors)

    fig.savefig(results_dir / "charts" / "dashboard.png", dpi=180)
    plt.close(fig)


def main():
    parser = argparse.ArgumentParser(description="Generate benchmark charts from result JSON files.")
    parser.add_argument("results_dir", type=Path, help="Path to a benchmark result directory")
    args = parser.parse_args()

    results_dir = args.results_dir.resolve()
    charts_dir = results_dir / "charts"
    charts_dir.mkdir(parents=True, exist_ok=True)

    rows = load_rows(results_dir)
    if not rows:
        raise SystemExit(f"No benchmark JSON files found in {results_dir}")

    grouped = split_by_gateway(rows)
    colors = {"gomodel": "#0A84FF", "litellm": "#FF6B35"}

    plt.style.use("seaborn-v0_8-whitegrid")

    save_single_plot(
        results_dir,
        grouped,
        key="req_per_sec",
        title="Throughput vs Concurrency",
        ylabel="Req/s",
        filename="throughput_reqps.png",
        colors=colors,
    )
    save_single_plot(
        results_dir,
        grouped,
        key="p95_ms",
        title="P95 Latency vs Concurrency",
        ylabel="Milliseconds",
        filename="latency_p95_ms.png",
        colors=colors,
    )
    save_single_plot(
        results_dir,
        grouped,
        key="rss_mb",
        title="Memory (RSS Avg) vs Concurrency",
        ylabel="MB",
        filename="memory_rss_mb.png",
        colors=colors,
    )
    save_single_plot(
        results_dir,
        grouped,
        key="cpu_pct",
        title="CPU (Avg) vs Concurrency",
        ylabel="Percent",
        filename="cpu_avg_pct.png",
        colors=colors,
    )
    render_dashboard(results_dir, grouped, colors)

    print(f"Charts saved to {charts_dir}")


if __name__ == "__main__":
    main()
