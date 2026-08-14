import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { paraglideVitePlugin } from "@inlang/paraglide-js";
import path from "node:path";
import { paraglideOptions } from "./paraglide.config.js";

// The dashboard is served by the Go binary: `vite build` emits into
// internal/admin/dashboard/static/dist, which is embedded via go:embed and
// served under /admin/static/. The Go handler rewrites the asset prefix when
// the app is mounted under a base path.
//
// The base applies to production builds only: the dev server runs at "/" so
// SPA routes like /admin/dashboard/providers-config resolve via the history
// fallback instead of being nested under /admin/static/.
export default defineConfig(({ command }) => ({
  base: command === "build" ? "/admin/static/" : "/",
  plugins: [paraglideVitePlugin(paraglideOptions), svelte()],
  resolve: {
    alias: {
      $lib: path.resolve(__dirname, "src/lib"),
      $pages: path.resolve(__dirname, "src/pages"),
    },
  },
  build: {
    outDir: path.resolve(
      __dirname,
      "../../internal/admin/dashboard/static/dist",
    ),
    emptyOutDir: true,
    chunkSizeWarningLimit: 1024,
  },
  server: {
    // Local dev against a running gateway: `npm run dev` proxies API calls
    // to the Go server (default :8080, override with GOMODEL_DEV_PROXY).
    proxy: {
      "/admin": {
        target: process.env.GOMODEL_DEV_PROXY || "http://localhost:8080",
        changeOrigin: true,
        bypass: (req) => {
          // Keep the SPA and its assets served by Vite.
          if (
            req.url.startsWith("/admin/dashboard") ||
            req.url.startsWith("/admin/static/")
          ) {
            return req.url;
          }
          return undefined;
        },
      },
      "/v1": {
        target: process.env.GOMODEL_DEV_PROXY || "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
}));
