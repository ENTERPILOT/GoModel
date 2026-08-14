// Shared by the Vite plugin and the standalone compiler used before checks and
// tests, so every workflow generates the same Paraglide runtime.
export const paraglideOptions = Object.freeze({
  project: "./project.inlang",
  outdir: "./src/lib/paraglide",
  strategy: ["custom-dashboard", "preferredLanguage", "baseLocale"],
});
