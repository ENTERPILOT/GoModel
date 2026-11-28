// Username and password login for the dashboard (DASHBOARD_PASSWORD). The Go
// handler injects window.GOMODEL_PASSWORD_LOGIN when the login is configured;
// the session itself lives in an HttpOnly cookie this code never sees.

export function passwordLoginEnabled(scope = globalThis) {
  return scope?.GOMODEL_PASSWORD_LOGIN === true;
}

// signInFailure names why POST /admin/session refused a sign-in, so the dialog
// can say whether to retype the credentials or to wait.
export function signInFailure(status) {
  if (status === 401) return "invalid";
  if (status === 429) return "throttled";
  return "failed";
}

// sessionUsername reads the signed-in username from a /admin/session body, or
// "" when the body reports no session.
export function sessionUsername(data) {
  if (!data || data.authenticated !== true) return "";
  return String(data.username || "").trim();
}
