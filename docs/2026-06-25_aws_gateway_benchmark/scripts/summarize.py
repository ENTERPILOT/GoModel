#!/usr/bin/env python3
"""Normalize the raw benchmark JSON into human tables + one summary.json.

Reads a results directory produced by run-on-instance.sh:

  results/
    run1/ … runN/   latency trials (per target+variant JSON)   <- aggregated here
    sweep/          throughput-vs-concurrency capacity points
    <gw>_image.json <gw>_startup.json <gw>_resources.json
    meta.json

Latency is reported as the MEDIAN across trials with a min–max spread on the
noisy tail (p99) and on rps, so single-window jitter no longer drives the story.
Also emits overhead-vs-baseline, a capacity-sweep table (sustained req/s and the
saturation knee), startup latency, and rps-per-CPU% efficiency.

Back-compat: a flat results dir (no run* subdirs) is treated as a single trial.
Stdlib only.
"""
import argparse
import glob
import json
import os
import re
import statistics

TARGETS = ["baseline", "gomodel", "litellm", "portkey", "bifrost"]
VARIANTS = [
    ("chat", "nonstream"), ("chat", "stream"),
    ("responses", "nonstream"), ("responses", "stream"),
    ("messages", "nonstream"), ("messages", "stream"),
]


def load(path):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def run_dirs(rd):
    """Trial dirs: run* subdirs if present, else the flat dir (single trial)."""
    runs = sorted(glob.glob(os.path.join(rd, "run*")),
                  key=lambda p: int(re.sub(r"\D", "", os.path.basename(p)) or 0))
    return runs or [rd]


def med(xs):
    xs = [x for x in xs if isinstance(x, (int, float))]
    return statistics.median(xs) if xs else None


def spread(xs):
    xs = [x for x in xs if isinstance(x, (int, float))]
    return (min(xs), max(xs)) if xs else (None, None)


def fnum(v, dp=2):
    try:
        return f"{float(v):.{dp}f}"
    except (TypeError, ValueError):
        return "—"


# ── latency aggregation ───────────────────────────────────────────────────────
def collect(runs, target, dialect, mode):
    """All trial summaries for one (target, variant)."""
    out = []
    for r in runs:
        d = load(os.path.join(r, f"{target}_{dialect}_{mode}.json"))
        if d:
            out.append(d)
    return out


def agg_variant(trials):
    """Median (+ spread) of the metrics we care about across trials."""
    def field(path):
        vals = []
        for t in trials:
            cur = t
            for k in path:
                cur = (cur or {}).get(k) if isinstance(cur, dict) else None
            vals.append(cur)
        return vals

    p99s = field(["total_latency", "p99_ms"])
    rpss = field(["rps"])
    return {
        "trials": len(trials),
        "ok": sum(t.get("ok", 0) for t in trials),
        "failed": sum(t.get("failed", 0) for t in trials),
        "rps": med(rpss), "rps_spread": spread(rpss),
        "p50": med(field(["total_latency", "p50_ms"])),
        "p90": med(field(["total_latency", "p90_ms"])),
        "p99": med(p99s), "p99_spread": spread(p99s),
        "ttft_p50": med(field(["ttft", "p50_ms"])),
        "gap_p50": med(field(["inter_chunk", "p50_ms"])),
        "gap_p99": med(field(["inter_chunk", "p99_ms"])),
    }


# ── capacity sweep ────────────────────────────────────────────────────────────
def sweep_curve(rd, target):
    """{concurrency: rps} for a target, read from results/sweep/<t>_c<cc>.json."""
    curve = {}
    for p in glob.glob(os.path.join(rd, "sweep", f"{target}_c*.json")):
        m = re.search(r"_c(\d+)\.json$", p)
        d = load(p)
        if m and d and isinstance(d.get("rps"), (int, float)):
            curve[int(m.group(1))] = d["rps"]
    return dict(sorted(curve.items()))


def sweep_stats(curve):
    if not curve:
        return {}
    peak_c = max(curve, key=curve.get)
    peak = curve[peak_c]
    # saturation knee: lowest concurrency reaching >=95% of peak rps.
    knee = next((c for c in sorted(curve) if curve[c] >= 0.95 * peak), peak_c)
    return {"peak_rps": peak, "peak_c": peak_c, "knee_c": knee, "curve": curve}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results-dir", required=True)
    args = ap.parse_args()
    rd = args.results_dir

    meta = load(os.path.join(rd, "meta.json")) or {}
    runs = run_dirs(rd)
    present = sorted({os.path.basename(p).split("_")[0]
                      for p in glob.glob(os.path.join(runs[0], "*_*_*.json"))})
    targets = [t for t in TARGETS if t in present]

    summary = {"meta": meta, "trials": len(runs), "latency": {}, "capacity": {}, "resources": {}}

    print("\n" + "=" * 86)
    print("GATEWAY BENCHMARK SUMMARY")
    print("=" * 86)
    if meta:
        print(f"instance={meta.get('instance_type')} cpus={meta.get('cpus')} "
              f"N={meta.get('n_requests')} c={meta.get('concurrency')} "
              f"trials={meta.get('repeats', len(runs))}")
    print(f"(latency = median across {len(runs)} trial(s); p99/rps show [min–max])")

    # ── Latency (median across trials) ─────────────────────────────────────────
    base_p50 = {}
    for dialect, mode in VARIANTS:
        b = agg_variant(collect(runs, "baseline", dialect, mode))
        base_p50[(dialect, mode)] = b.get("p50")

    print("\nLATENCY  (ms; rps = completed req/s @ c={})".format(meta.get("concurrency", "?")))
    hdr = (f"{'target':9} {'variant':18} {'ok/fail':>11} {'rps':>7} {'p50':>7} "
           f"{'p99':>7} {'p99 range':>15} {'ttft':>7} {'ovhd':>7}")
    print(hdr); print("-" * len(hdr))
    for t in targets:
        summary["latency"][t] = {}
        for dialect, mode in VARIANTS:
            a = agg_variant(collect(runs, t, dialect, mode))
            if not a["trials"]:
                continue
            key = f"{dialect}/{mode}"
            ovhd = (a["p50"] - base_p50[(dialect, mode)]
                    if a["p50"] is not None and base_p50.get((dialect, mode)) is not None else None)
            lo, hi = a["p99_spread"]
            rng = f"{fnum(lo)}–{fnum(hi)}" if lo is not None else "—"
            print(f"{t:9} {key:18} {str(a['ok'])+'/'+str(a['failed']):>11} "
                  f"{fnum(a['rps'],0):>7} {fnum(a['p50']):>7} {fnum(a['p99']):>7} "
                  f"{rng:>15} {fnum(a['ttft_p50']):>7} {fnum(ovhd):>7}")
            a["overhead_p50"] = ovhd
            summary["latency"][t][key] = a
        print()

    # ── Capacity sweep ─────────────────────────────────────────────────────────
    print("CAPACITY  (chat non-stream; sustained req/s by concurrency)")
    sweep_targets = [t for t in TARGETS if sweep_curve(rd, t)]
    if sweep_targets:
        concs = sorted({c for t in sweep_targets for c in sweep_curve(rd, t)})
        hdrc = f"{'target':9} " + " ".join(f"c{c:>6}" for c in concs) + f" {'peak':>8} {'@c':>4} {'knee':>5}"
        print(hdrc); print("-" * len(hdrc))
        for t in sweep_targets:
            curve = sweep_curve(rd, t)
            s = sweep_stats(curve)
            row = f"{t:9} " + " ".join(f"{fnum(curve.get(c), 0):>7}" for c in concs)
            row += f" {fnum(s['peak_rps'],0):>8} {s['peak_c']:>4} {s['knee_c']:>5}"
            print(row)
            summary["capacity"][t] = s
    else:
        print("  (no sweep data)")
    print()

    # ── Resources / footprint ──────────────────────────────────────────────────
    print("RESOURCES  (per gateway; img_zip = compressed pull size)")
    hdr2 = (f"{'gateway':9} {'img_zip':>8} {'img_disk':>9} {'startup_s':>10} {'idle_mb':>9} "
            f"{'peak_mb':>9} {'avg_cpu%':>9} {'load_rps':>9} {'rps/cpu%':>9}")
    print(hdr2); print("-" * len(hdr2))
    for t in [x for x in targets if x != "baseline"]:
        img = load(os.path.join(rd, f"{t}_image.json")) or {}
        res = load(os.path.join(rd, f"{t}_resources.json")) or {}
        startup = load(os.path.join(rd, f"{t}_startup.json")) or {}
        ul = res.get("under_load", {})
        load_rps = res.get("load_rps") or 0
        cpu = ul.get("avg_cpu_pct") or 0
        eff = (load_rps / cpu) if cpu else None
        print(f"{t:9} {fnum(img.get('compressed_mb'),1):>8} {fnum(img.get('size_mb'),1):>9} "
              f"{fnum(startup.get('startup_s'),2):>10} "
              f"{fnum(res.get('idle_mem_mb'),1):>9} {fnum(ul.get('peak_mem_mb'),1):>9} "
              f"{fnum(cpu,1):>9} {fnum(load_rps,0):>9} {fnum(eff,1):>9}")
        summary["resources"][t] = {"image": img, "resources": res, "startup": startup,
                                   "rps_per_cpu_pct": eff}
    print()

    out = os.path.join(rd, "summary.json")
    with open(out, "w") as f:
        json.dump(summary, f, indent=2)
    md = write_markdown(rd, meta, runs, targets)
    print(f"wrote {out}\nwrote {md}")


def write_markdown(rd, meta, runs, targets):
    """Emit clean GitHub-flavored Markdown tables."""
    L = ["# Gateway Benchmark Summary\n"]
    if meta:
        L.append(f"`instance={meta.get('instance_type')} cpus={meta.get('cpus')} "
                 f"N={meta.get('n_requests')} c={meta.get('concurrency')} "
                 f"trials={meta.get('repeats', len(runs))}`\n")
    L.append(f"_Latency = median across {len(runs)} trial(s); p99 shows the min–max "
             "across trials. rps in the latency table is completed req/s at the "
             "fixed concurrency (latency-coupled); see the capacity table for "
             "sustained throughput._\n")

    base_p50 = {(d, m): agg_variant(collect(runs, "baseline", d, m)).get("p50")
                for d, m in VARIANTS}

    L.append("## Latency (ms, median of trials)\n")
    L.append("| target | variant | ok/fail | rps | p50 | p90 | p99 | p99 min–max | ttft p50 | gap p50 | overhead p50 |")
    L.append("|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
    for t in targets:
        for dialect, mode in VARIANTS:
            a = agg_variant(collect(runs, t, dialect, mode))
            if not a["trials"]:
                continue
            ovhd = (a["p50"] - base_p50[(dialect, mode)]
                    if a["p50"] is not None and base_p50.get((dialect, mode)) is not None else None)
            lo, hi = a["p99_spread"]
            rng = f"{fnum(lo)}–{fnum(hi)}" if lo is not None else "—"
            gap = fnum(a["gap_p50"]) if mode == "stream" else ""
            ttft = fnum(a["ttft_p50"]) if mode == "stream" else ""
            L.append(f"| {t} | {dialect}/{mode} | {a['ok']}/{a['failed']} | {fnum(a['rps'],0)} | "
                     f"{fnum(a['p50'])} | {fnum(a['p90'])} | {fnum(a['p99'])} | {rng} | "
                     f"{ttft} | {gap} | {fnum(ovhd)} |")
    L.append("")

    # capacity
    sweep_targets = [t for t in TARGETS if sweep_curve(rd, t)]
    if sweep_targets:
        concs = sorted({c for t in sweep_targets for c in sweep_curve(rd, t)})
        L.append("## Capacity (chat non-stream, sustained req/s by concurrency)\n")
        L.append("| target | " + " | ".join(f"c={c}" for c in concs) + " | peak rps | @c | knee c |")
        L.append("|---|" + "--:|" * (len(concs) + 3))
        for t in sweep_targets:
            curve = sweep_curve(rd, t)
            s = sweep_stats(curve)
            cells = " | ".join(fnum(curve.get(c), 0) for c in concs)
            L.append(f"| {t} | {cells} | {fnum(s['peak_rps'],0)} | {s['peak_c']} | {s['knee_c']} |")
        L.append("")

    L.append("## Resources\n")
    L.append("| gateway | image MB (compressed) | image MB (on-disk) | startup s | idle MB | peak MB | avg CPU % | load rps | rps/CPU% |")
    L.append("|---|--:|--:|--:|--:|--:|--:|--:|--:|")
    for t in [x for x in targets if x != "baseline"]:
        img = load(os.path.join(rd, f"{t}_image.json")) or {}
        res = load(os.path.join(rd, f"{t}_resources.json")) or {}
        startup = load(os.path.join(rd, f"{t}_startup.json")) or {}
        ul = res.get("under_load", {})
        load_rps = res.get("load_rps") or 0
        cpu = ul.get("avg_cpu_pct") or 0
        eff = (load_rps / cpu) if cpu else None
        L.append(f"| {t} | {fnum(img.get('compressed_mb'),1)} | {fnum(img.get('size_mb'),1)} | "
                 f"{fnum(startup.get('startup_s'),2)} | "
                 f"{fnum(res.get('idle_mem_mb'),1)} | {fnum(ul.get('peak_mem_mb'),1)} | "
                 f"{fnum(cpu,1)} | {fnum(load_rps,0)} | {fnum(eff,1)} |")
    L.append("")

    path = os.path.join(rd, "summary.md")
    with open(path, "w") as f:
        f.write("\n".join(L))
    return path


if __name__ == "__main__":
    main()
