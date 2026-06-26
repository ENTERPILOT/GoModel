"""HTTP client for the QA harness: JSON, multipart, and SSE, plus audit lookup.

Stdlib only (urllib). Every gateway call carries a unique X-Request-Id and a
run-scoped X-GoModel-User-Path so the matching audit entry can be found, which
is how the harness inspects what the gateway *recorded* it received and
returned (request/response bodies, provider, resolved model, usage).
"""
import json
import time
import urllib.error
import urllib.request
import uuid


class Result:
    """Captured outcome of one HTTP exchange."""

    def __init__(self):
        self.status = 0
        self.headers = {}
        self.request_id = ""
        self.json = None          # parsed JSON body (if any)
        self.text = None          # text body (non-JSON)
        self.raw = b""            # raw body bytes (binary, e.g. TTS audio)
        self.bytes = 0            # raw body length
        self.content_type = ""
        self.events = []          # parsed SSE event objects
        self.stream_text = ""     # assembled assistant text from a stream
        self.terminal = None      # terminal SSE marker seen ("[DONE]", "message_stop", ...)
        self.error = None         # transport-level exception text


class Client:
    def __init__(self, base_url, api_key, user_path, timeout=120):
        self.base = base_url.rstrip("/")
        self.api_key = api_key
        self.user_path = user_path
        self.timeout = timeout

    def _common_headers(self, request_id, extra):
        h = {
            "Authorization": f"Bearer {self.api_key}",
            "X-Request-ID": request_id,
            "X-GoModel-User-Path": self.user_path,
        }
        if extra:
            h.update(extra)
        return h

    # ── JSON / raw request, optionally streaming ────────────────────────────
    def send(self, method, path, body=None, headers=None, stream=False, raw_body=None):
        rid = "qa-" + uuid.uuid4().hex[:24]
        res = Result()
        res.request_id = rid
        url = self.base + path
        hdrs = self._common_headers(rid, headers)

        data = None
        if raw_body is not None:
            data = raw_body.encode("utf-8")
            hdrs.setdefault("Content-Type", "application/json")
        elif body is not None:
            data = json.dumps(body).encode("utf-8")
            hdrs["Content-Type"] = "application/json"

        req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
        try:
            resp = urllib.request.urlopen(req, timeout=self.timeout)
            self._capture(res, resp, stream)
        except urllib.error.HTTPError as e:
            res.status = e.code
            self._capture(res, e, stream=False)
        except Exception as e:  # noqa: BLE001 — surface any transport failure as ERROR
            res.error = f"{type(e).__name__}: {e}"
        return res

    # ── multipart/form-data (audio transcriptions) ──────────────────────────
    def send_multipart(self, path, fields, file_field, filename, file_bytes,
                       file_content_type, headers=None):
        rid = "qa-" + uuid.uuid4().hex[:24]
        res = Result()
        res.request_id = rid
        boundary = "----qa" + uuid.uuid4().hex
        parts = []
        for k, v in (fields or {}).items():
            parts.append(f"--{boundary}\r\n".encode())
            parts.append(f'Content-Disposition: form-data; name="{k}"\r\n\r\n'.encode())
            parts.append(f"{v}\r\n".encode())
        parts.append(f"--{boundary}\r\n".encode())
        parts.append(
            f'Content-Disposition: form-data; name="{file_field}"; filename="{filename}"\r\n'.encode())
        parts.append(f"Content-Type: {file_content_type}\r\n\r\n".encode())
        parts.append(file_bytes)
        parts.append(f"\r\n--{boundary}--\r\n".encode())
        data = b"".join(parts)

        hdrs = self._common_headers(rid, headers)
        hdrs["Content-Type"] = f"multipart/form-data; boundary={boundary}"
        req = urllib.request.Request(self.base + path, data=data, method="POST", headers=hdrs)
        try:
            resp = urllib.request.urlopen(req, timeout=self.timeout)
            self._capture(res, resp, stream=False)
        except urllib.error.HTTPError as e:
            res.status = e.code
            self._capture(res, e, stream=False)
        except Exception as e:  # noqa: BLE001
            res.error = f"{type(e).__name__}: {e}"
        return res

    # ── response capture ────────────────────────────────────────────────────
    def _capture(self, res, resp, stream):
        res.status = getattr(resp, "status", res.status) or res.status
        try:
            res.headers = {k.lower(): v for k, v in resp.headers.items()}
        except Exception:  # noqa: BLE001
            res.headers = {}
        res.request_id = res.headers.get("x-request-id", res.request_id)
        res.content_type = res.headers.get("content-type", "")

        if stream and "text/event-stream" in res.content_type:
            self._read_sse(res, resp)
            return

        raw = resp.read()
        res.raw = raw
        res.bytes = len(raw)
        if "application/json" in res.content_type:
            try:
                res.json = json.loads(raw.decode("utf-8"))
            except Exception:  # noqa: BLE001
                res.text = raw.decode("utf-8", "replace")
        elif res.content_type.startswith("text/"):
            res.text = raw.decode("utf-8", "replace")
        # binary (audio) bodies: only size + content-type are kept.

    def _read_sse(self, res, resp):
        for rawline in resp:
            line = rawline.decode("utf-8", "replace").rstrip("\n").rstrip("\r")
            if not line or line.startswith(":"):
                continue
            if not line.startswith("data:"):
                continue
            payload = line[len("data:"):].strip()
            if payload == "[DONE]":
                res.terminal = "[DONE]"
                continue
            try:
                ev = json.loads(payload)
            except Exception:  # noqa: BLE001
                continue
            res.events.append(ev)
            self._accumulate(res, ev)

    @staticmethod
    def _accumulate(res, ev):
        """Assemble assistant text across the three streaming dialects and note
        terminal markers."""
        etype = ev.get("type")
        if etype in ("response.completed", "message_stop", "response.output_text.done"):
            res.terminal = etype
        # chat.completions: choices[].delta.content
        for ch in ev.get("choices", []) or []:
            delta = ch.get("delta") or {}
            if isinstance(delta.get("content"), str):
                res.stream_text += delta["content"]
        # responses: output_text deltas
        if etype == "response.output_text.delta" and isinstance(ev.get("delta"), str):
            res.stream_text += ev["delta"]
        # anthropic messages: content_block_delta.text
        if etype == "content_block_delta":
            d = ev.get("delta") or {}
            if isinstance(d.get("text"), str):
                res.stream_text += d["text"]

    # ── audit lookup ────────────────────────────────────────────────────────
    def fetch_audit(self, request_id, attempts=6, delay=1.5):
        """Find the audit entry for a request_id (retrying for flush lag) and
        return the full detail entry, or None."""
        for i in range(attempts):
            entry_id = self._find_entry_id(request_id)
            if entry_id:
                detail = self._get_json(f"/admin/audit/detail?log_id={entry_id}")
                if detail:
                    return detail
            if i < attempts - 1:
                time.sleep(delay)
        return None

    def _find_entry_id(self, request_id):
        listing = self._get_json(f"/admin/audit/log?search={request_id}&limit=20")
        if not listing:
            return None
        for entry in listing.get("entries", []):
            if entry.get("request_id") == request_id:
                return entry.get("id")
        return None

    def _get_json(self, path):
        req = urllib.request.Request(
            self.base + path, method="GET",
            headers={"Authorization": f"Bearer {self.api_key}"})
        try:
            resp = urllib.request.urlopen(req, timeout=self.timeout)
            return json.loads(resp.read().decode("utf-8"))
        except Exception:  # noqa: BLE001
            return None
