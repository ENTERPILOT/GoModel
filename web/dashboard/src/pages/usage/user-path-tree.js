// User path tree for the usage filter: known paths grouped by their common
// prefix segments, so /team/alice/laptop and /team/alice/ci sit under
// /team -> /team/alice. Selecting a node filters its whole subtree, the same
// way the API treats every user_path value. Pure functions for node:test.

// normalizeUserPath mirrors the gateway's canonical form: leading slash, no
// empty or trailing segments. Returns "" for blank input.
export function normalizeUserPath(raw) {
  const segments = String(raw || "")
    .split("/")
    .map((segment) => segment.trim())
    .filter(Boolean);
  if (segments.length === 0) return String(raw || "").trim() ? "/" : "";
  return "/" + segments.join("/");
}

// buildUserPathTree turns a flat path list into nested nodes
// { path, name, children }, sorted by name. Intermediate prefixes become
// nodes even when no request used them directly.
export function buildUserPathTree(paths) {
  const root = { path: "/", name: "/", children: [] };
  const byPath = new Map([["/", root]]);
  for (const raw of paths || []) {
    const path = normalizeUserPath(raw);
    if (!path || path === "/") continue;
    let parent = root;
    let prefix = "";
    for (const segment of path.slice(1).split("/")) {
      prefix += "/" + segment;
      let node = byPath.get(prefix);
      if (!node) {
        node = { path: prefix, name: segment, children: [] };
        byPath.set(prefix, node);
        parent.children.push(node);
      }
      parent = node;
    }
  }
  sortTree(root);
  return root.children;
}

function sortTree(node) {
  node.children.sort((a, b) => a.name.localeCompare(b.name));
  node.children.forEach(sortTree);
}

// visibleUserPathRows flattens the tree into the rows the picker shows:
// { path, name, depth, hasChildren, expanded }. Without a query only
// expanded nodes open; with one, matching paths and their ancestors show,
// fully expanded.
export function visibleUserPathRows(tree, expanded, query) {
  const needle = String(query || "").trim().toLowerCase();
  const rows = [];
  const walk = (nodes, depth) => {
    for (const node of nodes) {
      if (needle && !subtreeMatches(node, needle)) continue;
      const hasChildren = node.children.length > 0;
      const open = hasChildren && (needle ? true : expanded.has(node.path));
      rows.push({ path: node.path, name: node.name, depth, hasChildren, expanded: open });
      if (open) walk(node.children, depth + 1);
    }
  };
  walk(tree || [], 0);
  return rows;
}

function subtreeMatches(node, needle) {
  return node.path.toLowerCase().includes(needle) || node.children.some((child) => subtreeMatches(child, needle));
}

// userPathCovered reports whether path is already inside a selected
// subtree, so the picker can show it as included without a separate pick.
export function userPathCovered(path, selected) {
  return (selected || []).some(
    (value) => value !== path && (value === "/" || path.startsWith(value + "/")),
  );
}

// customUserPath returns the typed query as a path to offer when it is not
// already a known node or selection, or "" when there is nothing to offer.
export function customUserPath(query, tree, selected) {
  const path = normalizeUserPath(query);
  if (!path || (selected || []).includes(path)) return "";
  const known = (nodes) => nodes.some((node) => node.path === path || known(node.children));
  return known(tree || []) ? "" : path;
}

// toggleUserPath adds or removes path from the selection.
export function toggleUserPath(selected, path) {
  const current = selected || [];
  return current.includes(path) ? current.filter((value) => value !== path) : current.concat(path);
}
