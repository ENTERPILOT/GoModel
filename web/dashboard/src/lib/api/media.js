// Media downloads for the audit log: stored audio and images are served by
// GET /admin/media/{id} behind the bearer token, so they are fetched here
// and handed to the page as Blobs.

import { apiFetch } from "./client.js";

// fetchMediaBlob downloads one stored object as a Blob; a non-2xx status
// rejects so the caller can show the object as unavailable.
export async function fetchMediaBlob(id) {
  const res = await apiFetch("/admin/media/" + encodeURIComponent(id));
  if (!res.ok) {
    throw new Error("media " + id + " unavailable: " + res.status);
  }
  return res.blob();
}
