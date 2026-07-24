import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    // Force runes mode everywhere. Without this, a component that happens to
    // use no runes silently compiles in legacy mode with coarse reactivity:
    // any bind: mutation invalidates the whole component's prop expressions,
    // re-running child effects on unchanged values (e.g. Modal re-firing its
    // [data-modal-autofocus] on every keystroke and stealing focus).
    runes: true,
  },
};
