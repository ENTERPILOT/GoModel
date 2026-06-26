#!/usr/bin/env python3
"""Capture how each gateway translates the SAME client request to the SAME mock.

For every (case, gateway) it records four artifacts:
  - client_request   : what we sent to the gateway (the "pure" request)
  - sent_body        : the body after per-gateway model rewrite
  - upstream         : the request(s) the gateway actually sent to the mock
                       (the TRANSLATED request) + the canned ("pure") response
  - client_response  : what the gateway returned to us (the TRANSLATED response)

The mock is reset before each call and requests are sent one at a time, so the
shared recorder attributes each upstream call to the gateway+case that made it.
Stdlib only. Output: output/captures.json.
"""
import argparse
import copy
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
MOCK = "http://localhost:9999"

# Per-gateway base URL is env-overridable (e.g. GOMODEL_BASE) so a local dev
# server on a default port doesn't force a clash.
GATEWAYS = {
    "gomodel": {"base": os.environ.get("GOMODEL_BASE", "http://localhost:18080")},
    "litellm": {"base": os.environ.get("LITELLM_BASE", "http://localhost:4000")},
    "portkey": {"base": os.environ.get("PORTKEY_BASE", "http://localhost:8787"),
                "headers": {"x-portkey-provider": "openai",
                            "x-portkey-custom-host": "http://mock:9999/v1"}},
    "bifrost": {"base": os.environ.get("BIFROST_BASE", "http://localhost:8089")},
}
ORDER = ["gomodel", "litellm", "portkey", "bifrost"]
DIALECT_PATH = {"chat": "/v1/chat/completions", "responses": "/v1/responses",
                "messages": "/v1/messages"}


def model_for(gw, m):
    return "openai/" + m if gw == "bifrost" else m


def path_for(gw, dialect):
    if gw == "bifrost" and dialect == "messages":
        return "/anthropic/v1/messages"
    return DIALECT_PATH[dialect]


def headers_for(gw):
    h = {"Content-Type": "application/json", "Authorization": "Bearer sk-bench-test-key",
         "anthropic-version": "2023-06-01"}
    h.update(GATEWAYS[gw].get("headers", {}))
    return h


# ── HTTP ─────────────────────────────────────────────────────────────────────
def post(url, headers, body, stream, timeout=30):
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST", headers=headers)
    out = {"status": 0, "content_type": "", "json": None, "text": None,
           "stream_events": 0, "stream_text": "", "terminal": None, "error": None}
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        _capture(out, resp, stream)
    except urllib.error.HTTPError as e:
        out["status"] = e.code
        _capture(out, e, stream=False)
    except Exception as e:  # noqa: BLE001
        out["error"] = f"{type(e).__name__}: {e}"
    return out


def _capture(out, resp, stream):
    out["status"] = getattr(resp, "status", out["status"]) or out["status"]
    try:
        out["content_type"] = resp.headers.get("content-type", "")
    except Exception:  # noqa: BLE001
        pass
    if stream and "text/event-stream" in out["content_type"]:
        for rawline in resp:
            line = rawline.decode("utf-8", "replace").strip()
            if not line.startswith("data:"):
                continue
            payload = line[5:].strip()
            if payload == "[DONE]":
                out["terminal"] = "[DONE]"
                continue
            out["stream_events"] += 1
            try:
                ev = json.loads(payload)
            except Exception:  # noqa: BLE001
                continue
            t = ev.get("type")
            if t in ("response.completed", "message_stop"):
                out["terminal"] = t
            for ch in ev.get("choices", []) or []:
                d = (ch.get("delta") or {}).get("content")
                if isinstance(d, str):
                    out["stream_text"] += d
            if t == "response.output_text.delta" and isinstance(ev.get("delta"), str):
                out["stream_text"] += ev["delta"]
            if t == "content_block_delta":
                td = (ev.get("delta") or {}).get("text")
                if isinstance(td, str):
                    out["stream_text"] += td
        return
    raw = resp.read()
    if "application/json" in out["content_type"]:
        try:
            out["json"] = json.loads(raw.decode("utf-8"))
        except Exception:  # noqa: BLE001
            out["text"] = raw.decode("utf-8", "replace")
    else:
        out["text"] = raw.decode("utf-8", "replace")[:4000]


def get_json(url, timeout=10):
    try:
        resp = urllib.request.urlopen(urllib.request.Request(url, method="GET"), timeout=timeout)
        return json.loads(resp.read().decode("utf-8"))
    except Exception:  # noqa: BLE001
        return None


def mock_reset():
    # Fail fast: a silently failed reset would attribute stale upstream calls to
    # the wrong gateway/case and corrupt the captured corpus.
    try:
        resp = urllib.request.urlopen(
            urllib.request.Request(MOCK + "/__reset", data=b"", method="POST"), timeout=5)
        status = getattr(resp, "status", 200) or 200
        resp.read()
    except Exception as e:  # noqa: BLE001
        sys.exit(f"mock reset failed ({MOCK}/__reset): {e} — aborting to avoid a corrupt corpus")
    if status >= 400:
        sys.exit(f"mock reset returned HTTP {status} ({MOCK}/__reset) — aborting to avoid a corrupt corpus")


def wait_ready(gw, tries=60):
    url = GATEWAYS[gw]["base"] + "/v1/chat/completions"
    body = {"model": model_for(gw, "gpt-4o-mini"),
            "messages": [{"role": "user", "content": "ping"}]}
    for _ in range(tries):
        r = post(url, headers_for(gw), body, stream=False, timeout=8)
        if r["status"] == 200:
            return True
        time.sleep(2)
    return False


# ── trimming (keep artifacts readable) ────────────────────────────────────────
def trim(obj, limit=1500):
    if isinstance(obj, str):
        return obj if len(obj) <= limit else obj[:limit] + f"…(+{len(obj) - limit})"
    if isinstance(obj, list):
        return [trim(x, limit) for x in obj]
    if isinstance(obj, dict):
        return {k: trim(v, limit) for k, v in obj.items()}
    return obj


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--corpus", default=os.path.join(HERE, "corpus.json"))
    ap.add_argument("--out", default=os.path.join(HERE, "output", "captures.json"))
    ap.add_argument("--gateways", default=",".join(ORDER))
    args = ap.parse_args()

    gateways = [g.strip() for g in args.gateways.split(",") if g.strip()]
    unknown = [g for g in gateways if g not in GATEWAYS]
    if unknown:
        ap.error(f"unknown gateway(s): {', '.join(unknown)}; valid options: {', '.join(ORDER)}")
    corpus = json.load(open(args.corpus, encoding="utf-8"))

    if get_json(MOCK + "/__log") is None:
        print(f"mock not reachable at {MOCK} (is the stack up? is MOCK_RECORD=1?)", file=sys.stderr)
        return 2

    print("waiting for gateways…")
    ready = {}
    for gw in gateways:
        ready[gw] = wait_ready(gw)
        print(f"  {gw:9} {'ready' if ready[gw] else 'NOT READY (will still attempt)'}")

    results = {"meta": {"gateways": gateways, "ready": ready}, "cases": {}}
    for case in corpus:
        cid, dialect, stream = case["id"], case["dialect"], case.get("stream", False)
        entry = {"note": case.get("note", ""), "dialect": dialect, "stream": stream,
                 "client_request": case["body"], "gateways": {}}
        print(f"\n{cid}  ({dialect}{', stream' if stream else ''})")
        for gw in gateways:
            body = copy.deepcopy(case["body"])
            body["model"] = model_for(gw, body["model"])
            url = GATEWAYS[gw]["base"] + path_for(gw, dialect)
            mock_reset()
            resp = post(url, headers_for(gw), body, stream)
            log = get_json(MOCK + "/__log") or {}
            ups = log.get("entries") or []   # mock returns null when no upstream call was made
            up_paths = ",".join(sorted({e.get("path", "?") for e in ups})) or "—"
            print(f"  {gw:9} http={resp['status'] or resp['error']:>4}  "
                  f"upstream={len(ups)} [{up_paths}]")
            entry["gateways"][gw] = {
                "sent_body": trim(body),
                "url": url,
                "client_response": {
                    "status": resp["status"], "content_type": resp["content_type"],
                    "error": resp["error"],
                    "json": trim(resp["json"]) if resp["json"] is not None else None,
                    "text": resp["text"],
                    "stream_events": resp["stream_events"],
                    "stream_text": trim(resp["stream_text"]) if resp["stream_text"] else "",
                    "terminal": resp["terminal"],
                },
                "upstream": trim(ups),
            }
        results["cases"][cid] = entry

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    json.dump(results, open(args.out, "w", encoding="utf-8"), indent=2)
    print(f"\nwrote {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
