"""Declarative assertion operators.

Each assertion object names exactly one operator plus optional metadata:

    {"path": "$.usage.total_tokens", "gt": 0}
    {"path": "$.choices[0].message.content", "not_empty": true}
    {"path": "$.system_fingerprint", "present": true, "hard": false}

`hard` (default true) decides whether a failure fails the case or is recorded
as a soft/quality signal. The caller locates the value (from a response body,
header, stream, or audit entry) and passes (found, value) here.
"""
import re

from .paths import json_type

# Operators that are meaningful even when the value is absent.
_ABSENCE_OPS = {"present", "absent"}


def _as_number(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def apply_operator(assertion, found, value):
    """Evaluate one assertion. Returns (ok: bool, reason: str)."""
    for op in assertion:
        if op in ("path", "field", "name", "hard", "note", "target"):
            continue
        expected = assertion[op]

        if op == "present":
            ok = found is expected if isinstance(expected, bool) else found
            return ok, f"present={found}"
        if op == "absent":
            return (not found), f"present={found}"

        # All remaining operators require the value to exist.
        if not found and op not in _ABSENCE_OPS:
            return False, "value not found"

        if op == "equals":
            return value == expected, f"{value!r} == {expected!r}"
        if op == "not_equals":
            return value != expected, f"{value!r} != {expected!r}"
        if op == "not_empty":
            empty = value is None or value == "" or value == [] or value == {}
            return (not empty), f"non-empty (got {_short(value)})"
        if op == "contains":
            return expected.lower() in str(value).lower(), f"contains {expected!r}"
        if op == "not_contains":
            return expected.lower() not in str(value).lower(), f"not contains {expected!r}"
        if op == "contains_any":
            hay = str(value).lower()
            hit = next((w for w in expected if str(w).lower() in hay), None)
            return hit is not None, f"any{expected} -> {hit!r}"
        if op == "contains_all":
            hay = str(value).lower()
            miss = [w for w in expected if str(w).lower() not in hay]
            return not miss, f"all present (missing {miss})"
        if op == "regex":
            return re.search(expected, str(value)) is not None, f"~ /{expected}/"
        if op in ("gt", "gte", "lt", "lte"):
            n, e = _as_number(value), _as_number(expected)
            if n is None or e is None:
                return False, f"non-numeric {value!r}"
            ok = {"gt": n > e, "gte": n >= e, "lt": n < e, "lte": n <= e}[op]
            return ok, f"{n} {op} {e}"
        if op == "type":
            return json_type(value) == expected, f"type {json_type(value)} == {expected}"
        if op == "length_gte":
            try:
                return len(value) >= expected, f"len {len(value)} >= {expected}"
            except TypeError:
                return False, f"no length: {value!r}"
        if op == "one_of":
            return value in expected, f"{value!r} in {expected}"

        return False, f"unknown operator {op!r}"

    return False, "empty assertion"


def is_hard(assertion):
    return assertion.get("hard", True)


def _short(value, n=60):
    s = repr(value)
    return s if len(s) <= n else s[: n - 1] + "…"
