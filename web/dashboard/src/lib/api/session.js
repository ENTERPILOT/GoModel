// Dashboard session routes (/admin/session), served only when the username
// and password login is configured. Signing in sets an HttpOnly cookie the
// browser then sends by itself; these calls only start, read and end it.

import { apiFetch } from "./client.js";
import { auth } from "$lib/stores/auth.svelte.js";
import {
  sessionUsername,
  signInFailure,
} from "$lib/stores/password-login.js";

const SESSION_PATH = "/admin/session";

async function readJSON(res) {
  try {
    return await res.json();
  } catch {
    return null;
  }
}

// signIn returns "" on success, or the failure kind from signInFailure.
// Network errors propagate.
export async function signIn(username, password) {
  const res = await apiFetch(SESSION_PATH, {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) return signInFailure(res.status);
  auth.startSession(sessionUsername(await readJSON(res)) || username);
  return "";
}

// signOut reports whether the session was cleared. Network errors propagate.
export async function signOut() {
  const res = await apiFetch(SESSION_PATH, { method: "DELETE" });
  if (!res.ok) return false;
  auth.endSession();
  return true;
}

// loadSession restores the signed-in username after a page load. A failed
// request leaves the dashboard as it is: the next admin request settles it.
export async function loadSession() {
  if (!auth.passwordLogin) return;
  const generation = auth.generation;
  try {
    const res = await apiFetch(SESSION_PATH);
    if (!res.ok || generation < auth.generation) return;
    auth.sessionUser = sessionUsername(await readJSON(res));
  } catch {
    // Offline or restarting gateway.
  }
}
