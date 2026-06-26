"""Reporting: console table, results.json, and a Markdown report.

The report "registers" each case — the request as sent, the response and how
the gateway recorded/normalized it (from the audit entry), and every assertion
with its observed value — and "rates" it PASS / FAIL / ERROR / SKIP plus a
0–100 quality score for soft modality checks.
"""
import json
import os

STATUS_GLYPH = {"PASS": "PASS", "FAIL": "FAIL", "ERROR": "ERR ", "SKIP": "skip"}


def quality_score(case_result):
    soft = [c for c in case_result["checks"] if not c["hard"]]
    if not soft:
        return None
    return round(100 * sum(1 for c in soft if c["ok"]) / len(soft))


def print_console(results, meta):
    print("\n" + "=" * 92)
    print("GOMODEL QUALITY (QA) SUITE")
    print("=" * 92)
    print(f"gateway={meta['gateway']}  cases={len(results)}  "
          f"audit_bodies={'on' if meta['audit_bodies'] else 'OFF'}")
    print("-" * 92)
    hdr = f"{'status':6} {'id':46} {'prov':9} {'http':>4} {'qual':>5}  detail"
    print(hdr)
    print("-" * 92)
    for r in results:
        q = quality_score(r)
        qs = f"{q:>4}%" if q is not None else "   - "
        detail = r["detail"]
        if len(detail) > 24:
            detail = detail[:23] + "…"
        print(f"{STATUS_GLYPH.get(r['status'], r['status']):6} "
              f"{r['id'][:46]:46} {(r.get('provider') or ''):9} "
              f"{(r['http'] or ''):>4} {qs:>5}  {detail}")

    counts = _counts(results)
    print("-" * 92)
    print(f"PASS {counts['PASS']}   FAIL {counts['FAIL']}   "
          f"ERROR {counts['ERROR']}   SKIP {counts['SKIP']}   "
          f"(total {len(results)})")
    _print_breakdown("by endpoint", results, "group")
    _print_breakdown("by provider", results, "provider")
    print("=" * 92)


def _counts(results):
    c = {"PASS": 0, "FAIL": 0, "ERROR": 0, "SKIP": 0}
    for r in results:
        c[r["status"]] = c.get(r["status"], 0) + 1
    return c


def _print_breakdown(label, results, key):
    groups = {}
    for r in results:
        g = r.get(key) or "?"
        groups.setdefault(g, {"PASS": 0, "FAIL": 0, "ERROR": 0, "SKIP": 0})
        groups[g][r["status"]] += 1
    line = "  ".join(
        f"{g}:{v['PASS']}/{v['PASS'] + v['FAIL'] + v['ERROR'] + v['SKIP']}"
        for g, v in sorted(groups.items()))
    print(f"{label:12}: {line}")


def write_results(out_dir, results, meta):
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, "results.json"), "w", encoding="utf-8") as f:
        json.dump({"meta": meta, "counts": _counts(results), "cases": results},
                  f, indent=2)
    _write_markdown(out_dir, results, meta)
    return out_dir


def _write_markdown(out_dir, results, meta):
    c = _counts(results)
    L = ["# GoModel Quality (QA) Report\n",
         f"`gateway={meta['gateway']}  cases={len(results)}  "
         f"audit_bodies={'on' if meta['audit_bodies'] else 'off'}`\n",
         f"**PASS {c['PASS']} · FAIL {c['FAIL']} · ERROR {c['ERROR']} · SKIP {c['SKIP']}**\n",
         "| status | id | endpoint | provider | modality | http | quality | detail |",
         "|---|---|---|---|--:|--:|--:|---|"]
    for r in results:
        q = quality_score(r)
        qs = f"{q}%" if q is not None else ""
        modality = ",".join(r.get("modality") or [])
        L.append(f"| {r['status']} | `{r['id']}` | {r.get('group','')} | "
                 f"{r.get('provider','')} | {modality} | {r['http'] or ''} | {qs} | "
                 f"{_md(r['detail'])} |")
    L.append("")
    L.append("## Failed & errored cases\n")
    bad = [r for r in results if r["status"] in ("FAIL", "ERROR")]
    if not bad:
        L.append("_None._\n")
    for r in bad:
        L.append(f"### `{r['id']}` — {r['status']}\n")
        L.append(f"- {_md(r.get('title',''))}")
        L.append(f"- http `{r['http']}`  ·  {_md(r['detail'])}")
        for chk in r["checks"]:
            if not chk["ok"] and chk["hard"]:
                L.append(f"  - FAIL `{chk['where']}` — {_md(chk['reason'])}")
        L.append("")
    with open(os.path.join(out_dir, "report.md"), "w", encoding="utf-8") as f:
        f.write("\n".join(L))


def _md(s):
    return str(s).replace("|", "\\|").replace("\n", " ")
