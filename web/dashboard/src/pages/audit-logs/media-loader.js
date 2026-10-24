// Loads the media objects an audit body references by media_id. The admin
// API requires the bearer token, which <audio src> and <img src> cannot
// carry, so the bytes are fetched with the API client (see
// $lib/api/media.js) and handed to the element as an object URL. See
// docs/adr/0013-media-storage.md. This module has no dashboard imports so
// the Node test suite can load it.

// loadMedia fills every [data-media-id] element under root: <a> gets an
// href, everything else a src. Elements sharing an id share one download.
// A failed download hides the element and reveals the sibling
// .audit-media-unavailable note. Returns a cleanup that revokes the object
// URLs; call it when the markup is replaced or removed.
//
// fetchMedia downloads one id as a Blob; createObjectURL and revokeObjectURL
// default to the browser's and are injectable for tests.
export function loadMedia(root, { fetchMedia, createObjectURL, revokeObjectURL } = {}) {
  if (!root || typeof root.querySelectorAll !== "function" || typeof fetchMedia !== "function") {
    return () => {};
  }
  const create = createObjectURL || ((blob) => URL.createObjectURL(blob));
  const revoke = revokeObjectURL || ((url) => URL.revokeObjectURL(url));
  const urls = [];
  const downloads = new Map();
  let disposed = false;

  for (const node of root.querySelectorAll("[data-media-id]")) {
    const id = node.getAttribute("data-media-id");
    if (!id || node.getAttribute("data-media-state")) continue;
    node.setAttribute("data-media-state", "loading");
    let download = downloads.get(id);
    if (!download) {
      download = fetchMedia(id).then((blob) => {
        const url = create(blob);
        if (disposed) {
          // The pane went away while downloading: nothing will use the URL.
          revoke(url);
          return null;
        }
        urls.push(url);
        return url;
      });
      downloads.set(id, download);
    }
    download.then(
      (url) => {
        if (disposed || !url) return;
        if (node.tagName !== "A" && typeof node.addEventListener === "function") {
          // A download can succeed and still not decode (corrupt or
          // mislabeled bytes); that is unavailable too.
          node.addEventListener("error", () => markUnavailable(node), { once: true });
        }
        node.setAttribute(node.tagName === "A" ? "href" : "src", url);
        node.setAttribute("data-media-state", "loaded");
      },
      () => {
        if (disposed) return;
        markUnavailable(node);
      },
    );
  }

  return () => {
    disposed = true;
    for (const url of urls.splice(0)) revoke(url);
  };
}

function markUnavailable(node) {
  node.setAttribute("data-media-state", "unavailable");
  node.setAttribute("hidden", "");
  const parent = node.parentElement;
  const container = parent && parent.tagName === "A" ? parent.parentElement : parent;
  const note = container && typeof container.querySelector === "function"
    ? container.querySelector(".audit-media-unavailable")
    : null;
  if (note) note.removeAttribute("hidden");
}
