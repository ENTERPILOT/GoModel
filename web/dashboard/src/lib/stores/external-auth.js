const LOGIN_HEADER = "X-GoModel-Auth-Login";
const LOGOUT_HEADER = "X-GoModel-Auth-Logout";
const USER_HEADER = "X-GoModel-Auth-User";

export function safeAuthenticationPath(value) {
  const path = String(value || "").trim();
  if (!path.startsWith("/") || path.startsWith("//") || path.includes("\\")) {
    return "";
  }
  return path;
}

export function authenticationLoginURL(loginPath, location = globalThis.location) {
  const loginURL = safeAuthenticationPath(loginPath);
  const returnPath = safeAuthenticationPath(location?.pathname);
  if (!loginURL || !returnPath) return loginURL;

  const search = String(location?.search || "");
  const returnTo = returnPath + (search.startsWith("?") ? search : "");
  const separator = loginURL.includes("?") ? "&" : "?";
  return `${loginURL}${separator}return_to=${encodeURIComponent(returnTo)}`;
}

export function authenticationResponseMetadata(response) {
  const headers = response?.headers;
  if (!headers || typeof headers.has !== "function") return null;

  const metadata = {};
  if (headers.has(LOGIN_HEADER)) {
    metadata.loginURL = safeAuthenticationPath(headers.get(LOGIN_HEADER));
  }
  if (headers.has(LOGOUT_HEADER)) {
    metadata.logoutURL = safeAuthenticationPath(headers.get(LOGOUT_HEADER));
  }
  if (headers.has(USER_HEADER)) {
    metadata.user = String(headers.get(USER_HEADER) || "").trim().slice(0, 256);
  }
  return metadata;
}
