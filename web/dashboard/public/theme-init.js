// Resolve the theme before the first paint so the dashboard never flashes
// the wrong palette. This mirrors themeStore.apply() in
// src/lib/stores/ui.svelte.js: "system" resolves to the OS preference and
// data-theme always ends up a concrete "light" or "dark".
//
// Served as a classic external script from public/ and referenced in head
// before the stylesheet, so it executes synchronously, before first paint.
// The dashboard CSP allows no inline scripts at all — the runtime globals
// the Go server injects are the one hash-pinned exception — so this logic
// cannot live in an inline <script> in index.html.
(function () {
  function osMode() {
    return window.matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  }
  var mode;
  try {
    var stored = localStorage.getItem("gomodel_theme");
    mode = stored === "light" || stored === "dark" ? stored : osMode();
  } catch (err) {
    /* Storage blocked (cookies disabled etc.): reading is as
       unavailable as writing, so resolve from the OS preference —
       the same value themeStore will land on. */
    mode = osMode();
  }
  document.documentElement.setAttribute("data-theme", mode);
})();
