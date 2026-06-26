"""Config loading: master key, model/image roles, spec files.

The spec never hardcodes a concrete model id. Cases reference logical roles
("@openai.chat", "@anthropic.thinking", "@image.red") that resolve through
`models.json`, so a user adapts the whole corpus to their account by editing
one file.
"""
import glob
import json
import os

from .paths import png_base64, png_data_url

_COLORS = {"red": (220, 30, 30), "blue": (30, 60, 220), "green": (30, 180, 70)}

# @image.<name>   -> data: URL (chat/responses image_url form)
# @imageb64.<name> -> raw base64 (native Anthropic image source.data)
IMAGES = {name: png_data_url(rgb) for name, rgb in _COLORS.items()}
IMAGES_B64 = {name: png_base64(rgb) for name, rgb in _COLORS.items()}


def load_master_key(repo_root):
    """Master/admin key: env first, then the repo .env (never printed)."""
    key = os.environ.get("GOMODEL_API_KEY") or os.environ.get("GOMODEL_MASTER_KEY")
    if key:
        return key.strip()
    env_path = os.path.join(repo_root, ".env")
    if os.path.exists(env_path):
        with open(env_path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line.startswith("GOMODEL_MASTER_KEY="):
                    return line.split("=", 1)[1].strip().strip('"').strip("'")
    return ""


def load_models(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def load_specs(spec_dir, only=None):
    """Load and concatenate every spec/*.json (sorted by filename, then array
    order). `only` filters by substring against id / group / provider."""
    cases = []
    for path in sorted(glob.glob(os.path.join(spec_dir, "*.json"))):
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
        for case in data:
            case.setdefault("group", os.path.splitext(os.path.basename(path))[0])
            cases.append(case)
    if only:
        needle = only.lower()
        cases = [c for c in cases
                 if needle in c.get("id", "").lower()
                 or needle in c.get("group", "").lower()
                 or needle in c.get("provider", "").lower()]
    return cases


def resolve_roles(obj, models):
    """Recursively replace @provider.role and @image.name tokens with concrete
    values. Returns (resolved_obj, unresolved_roles)."""
    unresolved = []

    def walk(node):
        if isinstance(node, str):
            if node.startswith("@imageb64."):
                name = node[len("@imageb64."):]
                if name in IMAGES_B64:
                    return IMAGES_B64[name]
                unresolved.append(node)
                return node
            if node.startswith("@image."):
                name = node[len("@image."):]
                if name in IMAGES:
                    return IMAGES[name]
                unresolved.append(node)
                return node
            if node.startswith("@"):
                parts = node[1:].split(".")
                cur = models
                for p in parts:
                    if isinstance(cur, dict) and p in cur:
                        cur = cur[p]
                    else:
                        unresolved.append(node)
                        return node
                return cur
            return node
        if isinstance(node, list):
            return [walk(x) for x in node]
        if isinstance(node, dict):
            return {k: walk(v) for k, v in node.items()}
        return node

    return walk(obj), unresolved


def interpolate_vars(obj, variables):
    """Replace ${var} occurrences inside any string using captured runtime vars."""
    def walk(node):
        if isinstance(node, str):
            out = node
            for name, val in variables.items():
                out = out.replace("${" + name + "}", str(val))
            return out
        if isinstance(node, list):
            return [walk(x) for x in node]
        if isinstance(node, dict):
            return {k: walk(v) for k, v in node.items()}
        return node

    return walk(obj)
