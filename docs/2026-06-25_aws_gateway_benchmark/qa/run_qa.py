#!/usr/bin/env python3
"""GoModel quality (QA) harness — declarative spec runner.

Sends a curated corpus of complex requests through a running GoModel gateway to
real providers (OpenAI / Anthropic / Gemini) across every dialect and modality,
then registers and rates each case:

  - registers the request as sent, the response, and how the gateway *recorded*
    and normalized it (pulled from the audit log: inbound body, normalized body,
    provider, resolved model, usage);
  - rates each case PASS / FAIL / ERROR / SKIP, plus a 0–100 quality score for
    soft modality checks (did the vision model name the colour, did STT recover
    the spoken words, …).

Usage:
  python run_qa.py                         # full corpus against localhost:8080
  python run_qa.py --only chat             # filter by id/group/provider substring
  python run_qa.py --only openai --no-audit
  python run_qa.py --list                  # list cases without running
  python run_qa.py --gateway http://host:8080 --models models.json

Requires the gateway running with audit logging + bodies for the preservation
checks:  LOGGING_ENABLED=true LOGGING_LOG_BODIES=true LOGGING_LOG_HEADERS=true
LOGGING_LOG_AUDIO_BODIES=true  (see README).  Stdlib only.
"""
import argparse
import os
import sys
import time
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

from qalib import config, report          # noqa: E402
from qalib.assertions import apply_operator, is_hard  # noqa: E402
from qalib.client import Client           # noqa: E402
from qalib.paths import get_path          # noqa: E402

def _find_repo_root(start):
    """Walk up to the repo root (the dir holding .git), for the .env lookup."""
    d = start
    while d != os.path.dirname(d):
        if os.path.exists(os.path.join(d, ".git")):
            return d
        d = os.path.dirname(d)
    return start


REPO_ROOT = _find_repo_root(HERE)


def locate(target, res, audit):
    """Resolve a quality/assertion target selector to (found, value)."""
    if target == "stream":
        return bool(res.stream_text), res.stream_text
    if target == "body.text":
        return res.text is not None, res.text
    if target.startswith("response:"):
        return get_path(res.json, target[len("response:"):])
    if target.startswith("audit:"):
        if audit is None:
            return False, None
        return get_path(audit, target[len("audit:"):])
    return False, None


def evaluate(case, res, audit, audit_attempted, variables=None):
    """Return (status, checks, detail). checks: [{where, ok, hard, reason}]."""
    checks = []
    expect = case.get("expect", {})
    if variables:
        # Resolve ${var} references (e.g. a captured ${conversation_id}) in
        # assertion operands, the same way request paths/bodies are interpolated.
        expect = config.interpolate_vars(expect, variables)

    if res.error:
        return "ERROR", checks, res.error

    # ── status ──────────────────────────────────────────────────────────────
    want = expect.get("status", 200)
    want = want if isinstance(want, list) else [want]
    checks.append({"where": "status", "ok": res.status in want, "hard": True,
                   "reason": f"{res.status} in {want}"})

    # ── headers ───────────────────────────────────────────────────────────────
    for a in expect.get("headers", []):
        name = a["name"].lower()
        found = name in res.headers
        ok, reason = apply_operator(a, found, res.headers.get(name))
        checks.append({"where": f"header:{a['name']}", "ok": ok,
                       "hard": is_hard(a), "reason": reason})

    # ── body (synthetic fields for any body, incl. binary) ────────────────────
    body_fields = {"content_type": res.content_type, "bytes": res.bytes,
                   "text": res.text}
    for a in expect.get("body", []):
        field = a["field"]
        val = body_fields.get(field)
        ok, reason = apply_operator(a, val is not None, val)
        checks.append({"where": f"body:{field}", "ok": ok,
                       "hard": is_hard(a), "reason": reason})

    # ── response JSON ─────────────────────────────────────────────────────────
    for a in expect.get("response", []):
        found, val = get_path(res.json, a["path"]) if res.json is not None else (False, None)
        ok, reason = apply_operator(a, found, val)
        checks.append({"where": f"response:{a['path']}", "ok": ok,
                       "hard": is_hard(a), "reason": reason})

    # ── streaming ─────────────────────────────────────────────────────────────
    st = expect.get("stream")
    if st:
        if "min_events" in st:
            n = len(res.events)
            checks.append({"where": "stream:events", "ok": n >= st["min_events"],
                           "hard": True, "reason": f"{n} events >= {st['min_events']}"})
        if "terminal" in st:
            checks.append({"where": "stream:terminal", "ok": res.terminal == st["terminal"],
                           "hard": True, "reason": f"{res.terminal!r} == {st['terminal']!r}"})
        for et in st.get("event_types", []):
            present = any(e.get("type") == et for e in res.events)
            checks.append({"where": f"stream:type:{et}", "ok": present,
                           "hard": True, "reason": f"event {et} present={present}"})
        for a in st.get("text", []):
            ok, reason = apply_operator(a, bool(res.stream_text), res.stream_text)
            checks.append({"where": "stream:text", "ok": ok,
                           "hard": is_hard(a), "reason": reason})

    # ── audit (gateway's own record of what it received / returned) ───────────
    for a in expect.get("audit", []):
        path = a["path"]
        if not audit_attempted:
            continue
        if audit is None:
            checks.append({"where": f"audit:{path}", "ok": True, "hard": False,
                           "reason": "audit entry not found (skipped)"})
            continue
        found, val = get_path(audit, path)
        # If body capture is off, demote data.* checks to soft skips.
        if not found and path.startswith("$.data."):
            data = audit.get("data") or {}
            if "request_body" not in data and "response_body" not in data:
                checks.append({"where": f"audit:{path}", "ok": True, "hard": False,
                               "reason": "audit bodies off (enable LOGGING_LOG_BODIES)"})
                continue
        ok, reason = apply_operator(a, found, val)
        checks.append({"where": f"audit:{path}", "ok": ok,
                       "hard": is_hard(a), "reason": reason})

    # ── quality (always soft; feeds the score) ────────────────────────────────
    for a in expect.get("quality", []):
        found, val = locate(a.get("target", "stream"), res, audit)
        a = dict(a)
        a["hard"] = False
        ok, reason = apply_operator(a, found, val)
        checks.append({"where": f"quality:{a.get('target','stream')}", "ok": ok,
                       "hard": False, "reason": reason})

    hard_fail = [c for c in checks if c["hard"] and not c["ok"]]
    status = "FAIL" if hard_fail else "PASS"
    if hard_fail:
        detail = f"{hard_fail[0]['where']}: {hard_fail[0]['reason']}"
    else:
        ok_n = sum(1 for c in checks if c["ok"])
        detail = f"{ok_n}/{len(checks)} ok"
    return status, checks, detail


def run_case(case, client, models, variables, do_audit):
    """Build, send, capture vars, fetch audit for one case. Returns (res, audit,
    audit_attempted, skip_reason)."""
    resolved, unresolved = config.resolve_roles(case.get("request", {}), models)
    if unresolved:
        return None, None, False, f"unresolved role(s): {', '.join(sorted(set(unresolved)))}"
    req = config.interpolate_vars(resolved, variables)

    produce = req.get("produce")
    if produce == "tts_then_stt":
        res = _produce_tts_then_stt(req, client)
    else:
        res = client.send(req.get("method", "POST"), req["path"], body=req.get("body"),
                          headers=req.get("headers"), stream=req.get("stream", False),
                          raw_body=req.get("raw_body"))

    # capture runtime vars from the response body
    for name, path in (case.get("capture") or {}).items():
        if res.json is not None:
            found, val = get_path(res.json, path)
            if found:
                variables[name] = val

    audit_attempted = bool(do_audit and case.get("expect", {}).get("audit"))
    audit = client.fetch_audit(res.request_id) if audit_attempted else None
    return res, audit, audit_attempted, None


def _produce_tts_then_stt(req, client):
    tts = req["tts"]
    fmt = tts.get("response_format", "mp3")
    r1 = client.send("POST", "/v1/audio/speech", body=tts)
    if r1.status != 200 or not r1.raw:
        r1.error = f"tts produce failed (status {r1.status}, {r1.bytes} bytes)"
        return r1
    stt = req["stt"]
    mime = r1.content_type or "audio/mpeg"
    res = client.send_multipart("/v1/audio/transcriptions", stt, "file",
                                f"qa.{fmt}", r1.raw, mime)
    res.produced_from = {"tts_status": r1.status, "tts_bytes": r1.bytes,
                         "tts_content_type": r1.content_type}
    return res


def _trim(obj, limit=4000):
    """Trim long strings (base64 audio, etc.) so the artifact stays readable."""
    if isinstance(obj, str):
        return obj if len(obj) <= limit else obj[:limit] + f"…(+{len(obj) - limit} chars)"
    if isinstance(obj, list):
        return [_trim(x, limit) for x in obj]
    if isinstance(obj, dict):
        return {k: _trim(v, limit) for k, v in obj.items()}
    return obj


def artifact(case, res, audit):
    """The registered record: what was sent, what came back, how the gateway
    recorded/normalized it."""
    if res is None:
        return {"request": case.get("request"), "response": None, "audit": None}
    resp = {"status": res.status, "content_type": res.content_type,
            "bytes": res.bytes, "request_id": res.request_id}
    if res.json is not None:
        resp["json"] = _trim(res.json)
    if res.text is not None:
        resp["text"] = _trim(res.text)
    if res.events:
        resp["stream_events"] = len(res.events)
        resp["stream_text"] = _trim(res.stream_text)
        resp["terminal"] = res.terminal
    if getattr(res, "produced_from", None):
        resp["produced_from"] = res.produced_from
    audit_view = None
    if audit:
        data = audit.get("data") or {}
        audit_view = {
            "provider": audit.get("provider"),
            "resolved_model": audit.get("resolved_model"),
            "requested_model": audit.get("requested_model"),
            "status_code": audit.get("status_code"),
            "duration_ns": audit.get("duration_ns"),
            "usage": audit.get("usage"),
            "request_body": _trim(data.get("request_body")),
            "response_body": _trim(data.get("response_body")),
        }
    return {"request": _trim(case.get("request")), "response": resp, "audit": audit_view}


def main():
    ap = argparse.ArgumentParser(description="GoModel quality (QA) harness")
    ap.add_argument("--gateway", default=os.environ.get("GATEWAY", "http://localhost:8080"))
    ap.add_argument("--models", default=os.path.join(HERE, "models.json"))
    ap.add_argument("--spec-dir", default=os.path.join(HERE, "spec"))
    ap.add_argument("--out", default=os.path.join(HERE, "output"))
    ap.add_argument("--only", default=None, help="filter by id/group/provider substring")
    ap.add_argument("--no-audit", action="store_true", help="skip audit-log cross-checks")
    ap.add_argument("--list", action="store_true", help="list matching cases and exit")
    ap.add_argument("--timeout", type=int, default=120)
    args = ap.parse_args()

    models = config.load_models(args.models)
    cases = config.load_specs(args.spec_dir, args.only)
    if not cases:
        print("no cases matched", file=sys.stderr)
        return 2
    if args.list:
        for c in cases:
            print(f"{c['id']:48} {c.get('group',''):14} {c.get('provider','')}")
        print(f"\n{len(cases)} cases")
        return 0

    key = config.load_master_key(REPO_ROOT)
    if not key:
        print("no GOMODEL_MASTER_KEY found (env or repo .env)", file=sys.stderr)
        return 2

    run_id = uuid.uuid4().hex[:12]
    user_path = f"/qa/{run_id}"
    client = Client(args.gateway, key, user_path, timeout=args.timeout)

    health = client.send("GET", "/health")
    if health.error or health.status >= 500:
        print(f"gateway not reachable at {args.gateway}: "
              f"{health.error or health.status}", file=sys.stderr)
        return 2

    print(f"running {len(cases)} cases against {args.gateway}  (user_path {user_path})")
    results = []
    variables = {}
    audit_bodies_seen = False
    for case in cases:
        t0 = time.time()
        try:
            res, audit, attempted, skip = run_case(case, client, models, variables,
                                                    do_audit=not args.no_audit)

            if skip:
                results.append(_record(case, "SKIP", [], skip, res, audit, time.time() - t0))
                print(f"skip {case['id']}: {skip}")
                continue

            if audit and (audit.get("data") or {}).get("request_body") is not None:
                audit_bodies_seen = True

            status, checks, detail = evaluate(case, res, audit, attempted, variables)
            rec = _record(case, status, checks, detail, res, audit, time.time() - t0)
            results.append(rec)
            print(f"{report.STATUS_GLYPH.get(status, status):4} {case['id']}: {detail}")
        except Exception as e:  # noqa: BLE001 — never let one case abort the run
            err = f"{type(e).__name__}: {e}"
            results.append(_record(case, "ERROR", [], err, None, None, time.time() - t0))
            print(f"ERR  {case['id']}: {err}")
            continue

    meta = {"gateway": args.gateway, "run_id": run_id, "user_path": user_path,
            "audit_bodies": audit_bodies_seen, "models": models}
    report.print_console(results, meta)
    out_dir = os.path.join(args.out, run_id)
    report.write_results(out_dir, results, meta)
    print(f"\nwrote {os.path.join(out_dir, 'results.json')}\n"
          f"wrote {os.path.join(out_dir, 'report.md')}")

    failed = sum(1 for r in results if r["status"] in ("FAIL", "ERROR"))
    return 1 if failed else 0


def _record(case, status, checks, detail, res, audit, elapsed):
    return {
        "id": case["id"], "title": case.get("title", ""), "group": case.get("group"),
        "provider": case.get("provider"), "modality": case.get("modality"),
        "status": status, "http": (res.status if res else None),
        "detail": detail, "elapsed_ms": round(elapsed * 1000),
        "checks": checks, "artifact": artifact(case, res, audit),
    }


if __name__ == "__main__":
    sys.exit(main())
