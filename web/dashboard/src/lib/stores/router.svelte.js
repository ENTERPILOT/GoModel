// Path-based client-side router for /admin/dashboard/{page}. Route aliases:
// /admin/dashboard/audit -> audit-logs,
// /admin/dashboard/settings/guardrails -> guardrails.

import { gomodelPath, unprefixedPath } from "$lib/api/paths.js";

export const PAGES = [
  "overview",
  "usage",
  "budgets",
  "rate-limits",
  "models",
  "workflows",
  "audit-logs",
  "guardrails",
  "mcp-servers",
  "providers-config",
  "auth-keys",
  "settings",
];

// stripViteBase removes the Vite base prefix when the app is served under
// one (defensive: shouldn't occur since dev runs at "/", but keeps routes
// working if the page is ever opened via the asset base path).
function stripViteBase(path) {
  const base = import.meta.env.BASE_URL || "/";
  if (base !== "/" && path.startsWith(base)) {
    return "/" + path.slice(base.length).replace(/^\/+/, "");
  }
  return path;
}

export function parseRoute(pathname) {
  const path = stripViteBase(unprefixedPath(pathname)).replace(/\/$/, "");
  const rest = path.replace("/admin/dashboard", "").replace(/^\//, "");
  const parts = rest.split("/");
  let page = parts[0];
  if (page === "audit") {
    page = "audit-logs";
  }
  const sub = parts[1] || null;
  if (page === "settings" && sub === "guardrails") {
    return { page: "guardrails", sub: null };
  }
  page = PAGES.includes(page) ? page : "overview";
  return { page, sub };
}

class Router {
  page = $state("overview");
  sub = $state(null);

  init() {
    const { page, sub } = parseRoute(window.location.pathname);
    this.page = page;
    this.sub = sub;
    window.addEventListener("popstate", () => {
      const { page: p, sub: s } = parseRoute(window.location.pathname);
      this.page = p;
      this.sub = s;
    });
  }

  navigate(page, sub = null) {
    const suffix = sub ? "/" + sub : "";
    history.pushState(
      null,
      "",
      gomodelPath("/admin/dashboard/" + page + suffix),
    );
    this.page = page;
    this.sub = sub;
  }
}

export const router = new Router();
