// Base-path helpers. The Go handler injects window.GOMODEL_BASE_PATH when the
// app is mounted under a sub-path; every URL the SPA touches goes through
// these helpers (fetch/history are never monkey-patched).

export function basePath(): string {
  if (typeof window === "undefined") return "/";
  return window.GOMODEL_BASE_PATH || "/";
}

// gomodelPath prefixes an absolute app path with the configured base path.
export function gomodelPath(urlPath: string): string {
  const base = basePath();
  if (
    !urlPath ||
    urlPath.charAt(0) !== "/" ||
    urlPath.indexOf("//") === 0 ||
    base === "/"
  ) {
    return urlPath;
  }
  if (urlPath === base || urlPath.indexOf(base + "/") === 0) {
    return urlPath;
  }
  return base + urlPath;
}

// unprefixedPath strips the base path from a location pathname.
export function unprefixedPath(path: string): string {
  const base = basePath();
  if (base === "/" || !path) {
    return path;
  }
  if (path === base) {
    return "/";
  }
  if (path.indexOf(base + "/") === 0) {
    return path.slice(base.length) || "/";
  }
  return path;
}

export function appVersion(): string {
  if (typeof window === "undefined") return "";
  return window.GOMODEL_VERSION || "";
}

export function demoMode(): boolean {
  if (typeof window === "undefined") return false;
  return window.GOMODEL_DEMO_MODE === true;
}
