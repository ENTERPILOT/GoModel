"""JSON-path mini-language and deterministic image fixtures.

The path language is intentionally tiny — enough to address normalized AI
responses and audit entries without a dependency:

    $                      the root object
    $.a.b                  nested object keys
    $.choices[0].message   array index
    $.data.request_body.x  arbitrary nested key (audit bodies)

`get_path` returns (found, value) so callers can distinguish "missing" from
"present but null/empty".
"""
import base64
import re
import struct
import zlib

_TOKEN = re.compile(r"([^.\[\]]+)|\[(\d+)\]")


def get_path(obj, path):
    """Resolve a `$.a.b[0]` path. Returns (found: bool, value)."""
    if path in ("$", "", None):
        return True, obj
    if path.startswith("$."):
        path = path[2:]
    elif path.startswith("$"):
        path = path[1:]
    cur = obj
    for key, idx in _TOKEN.findall(path):
        if idx != "":
            if not isinstance(cur, list):
                return False, None
            i = int(idx)
            if i >= len(cur):
                return False, None
            cur = cur[i]
        else:
            if not isinstance(cur, dict) or key not in cur:
                return False, None
            cur = cur[key]
    return True, cur


def json_type(value):
    """JSON type name for a Python value (for the `type` assertion)."""
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, (int, float)):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return "unknown"


# ── deterministic image fixtures ────────────────────────────────────────────
# A solid-colour PNG is the simplest reproducible vision input: every provider
# can name a colour, so `quality: contains_any [red]` is a stable smoke check
# that needs no network fetch and no binary asset checked into the repo.

def _solid_png(rgb, size=48):
    raw = bytearray()
    row = bytes(rgb) * size
    for _ in range(size):
        raw.append(0)            # PNG filter type 0 (none) per scanline
        raw.extend(row)

    def chunk(typ, data):
        body = typ + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    sig = b"\x89PNG\r\n\x1a\n"
    ihdr = struct.pack(">IIBBBBB", size, size, 8, 2, 0, 0, 0)  # 8-bit RGB
    idat = zlib.compress(bytes(raw), 9)
    return sig + chunk(b"IHDR", ihdr) + chunk(b"IDAT", idat) + chunk(b"IEND", b"")


def png_base64(rgb, size=48):
    return base64.b64encode(_solid_png(rgb, size)).decode("ascii")


def png_data_url(rgb, size=48):
    return "data:image/png;base64," + png_base64(rgb, size)
