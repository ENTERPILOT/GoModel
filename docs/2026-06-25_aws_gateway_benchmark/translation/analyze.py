#!/usr/bin/env python3
"""Glue for the AI translation analysis.

  analyze.py --split          read output/captures.json, write one self-contained
                              bundle per case to output/cases/<id>.json (the input
                              an AI analyst reviews for that case)
  analyze.py --render         read output/analysis/<id>.json (the AI's structured
                              verdict per case) + captures.json, write output/report.md

The actual case-by-case comparison is done by an AI analyst (one per case): it
reads a bundle and writes its verdict to output/analysis/<id>.json following the
schema documented in --split's banner. Stdlib only.
"""
import argparse
import glob
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "output")
GATEWAYS = ["gomodel", "litellm", "portkey", "bifrost"]

ANALYSIS_SCHEMA = {
    "case_id": "string",
    "verdict_per_gateway": {
        "<gateway>": {
            "reached_provider": "bool — did the gateway make an upstream call?",
            "upstream_path": "the path it called on the mock",
            "request_added": ["fields/headers the gateway ADDED vs the client request"],
            "request_dropped": ["client fields the gateway DROPPED before upstream"],
            "request_renamed": ["client->upstream field renames, e.g. max_tokens->max_completion_tokens"],
            "request_reshaped": "prose: structural changes (dialect translation, message shape, tool schema)",
            "response_extras_preserved": ["provider extras kept in the client response: system_fingerprint/service_tier/x_provider_note/usage"],
            "response_extras_dropped": ["provider extras the gateway stripped"],
            "response_reshaped": "prose: how the upstream response was renormalized for the client",
            "fidelity_score": "0-100 int: how faithfully intent was preserved end-to-end",
            "notes": "anything notable"
        }
    },
    "cross_gateway_findings": ["concise comparative observations"],
    "ranking": ["gateways best->worst fidelity for this case"],
}


def split():
    caps = json.load(open(os.path.join(OUT, "captures.json"), encoding="utf-8"))
    d = os.path.join(OUT, "cases")
    os.makedirs(d, exist_ok=True)
    ids = []
    for cid, case in caps["cases"].items():
        bundle = {"case_id": cid, "dialect": case["dialect"], "stream": case["stream"],
                  "intent_note": case["note"], "client_request": case["client_request"],
                  "gateways": case["gateways"]}
        json.dump(bundle, open(os.path.join(d, f"{cid}.json"), "w", encoding="utf-8"), indent=2)
        ids.append(cid)
    print(f"wrote {len(ids)} case bundles to {d}")
    for cid in ids:
        print("  ", cid)


def _cell(items):
    if not items:
        return "—"
    return "; ".join(str(x) for x in items)[:120]


def render():
    caps = json.load(open(os.path.join(OUT, "captures.json"), encoding="utf-8"))
    analyses = {}
    for p in glob.glob(os.path.join(OUT, "analysis", "*.json")):
        try:
            a = json.load(open(p, encoding="utf-8"))
            analyses[a.get("case_id", os.path.basename(p)[:-5])] = a
        except (OSError, ValueError):
            pass

    gws = caps["meta"]["gateways"]
    L = ["# Gateway translation-fidelity report\n",
         "Same request through each gateway, same mock provider. The AI analyst "
         "compared the translated upstream request vs the pure client request, and "
         "the translated client response vs the pure mock response, per case.\n",
         f"`gateways: {', '.join(gws)}`  ·  `cases: {len(caps['cases'])}`\n"]

    # ── aggregate scoreboard ──────────────────────────────────────────────────
    scores = {g: [] for g in gws}
    for a in analyses.values():
        for g, v in (a.get("verdict_per_gateway") or {}).items():
            s = v.get("fidelity_score")
            if isinstance(s, (int, float)):
                scores.setdefault(g, []).append(s)
    L.append("## Fidelity scoreboard (mean of per-case AI scores)\n")
    L.append("| gateway | mean fidelity | cases scored |")
    L.append("|---|--:|--:|")
    for g in gws:
        vals = scores.get(g, [])
        mean = round(sum(vals) / len(vals)) if vals else 0
        L.append(f"| {g} | {mean} | {len(vals)} |")
    L.append("")

    # ── per-case detail ────────────────────────────────────────────────────────
    for cid, case in caps["cases"].items():
        a = analyses.get(cid)
        L.append(f"## `{cid}` — {case['dialect']}{', stream' if case['stream'] else ''}\n")
        L.append(f"_{case['note']}_\n")
        if not a:
            L.append("> _no AI analysis recorded for this case_\n")
            continue
        L.append("| gateway | upstream | added | dropped | renamed | resp extras kept | resp dropped | fidelity |")
        L.append("|---|---|---|---|---|---|---|--:|")
        for g in gws:
            v = (a.get("verdict_per_gateway") or {}).get(g)
            if not v:
                L.append(f"| {g} | — | — | — | — | — | — | — |")
                continue
            L.append(f"| {g} | {v.get('upstream_path','—')} | {_cell(v.get('request_added'))} | "
                     f"{_cell(v.get('request_dropped'))} | {_cell(v.get('request_renamed'))} | "
                     f"{_cell(v.get('response_extras_preserved'))} | {_cell(v.get('response_extras_dropped'))} | "
                     f"{v.get('fidelity_score','—')} |")
        L.append("")
        if a.get("cross_gateway_findings"):
            L.append("**Findings:**")
            for f in a["cross_gateway_findings"]:
                L.append(f"- {f}")
            L.append("")
        if a.get("ranking"):
            L.append(f"**Fidelity ranking:** {' > '.join(a['ranking'])}\n")

    path = os.path.join(OUT, "report.md")
    open(path, "w", encoding="utf-8").write("\n".join(L))
    print(f"wrote {path}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--split", action="store_true")
    ap.add_argument("--render", action="store_true")
    ap.add_argument("--schema", action="store_true", help="print the analysis JSON schema")
    args = ap.parse_args()
    if args.schema:
        print(json.dumps(ANALYSIS_SCHEMA, indent=2))
    elif args.split:
        split()
    elif args.render:
        render()
    else:
        ap.error("one of --split / --render / --schema required")


if __name__ == "__main__":
    main()
