// Deployment base path for hand-written absolute URLs.
//
// Astro auto-prefixes paths it controls (imported assets, bundled scripts)
// with the configured `base`, but NOT hand-written href/src strings. On a
// GitHub project page the site serves under /<repo>/ (e.g. /sdk/), so every
// hand-written absolute internal URL must be prefixed or it 404s (ADR 0009
// §accessible). At a custom-domain root the base is "" and these are no-ops.
//
// Only imported by .astro components (Vite injects import.meta.env.BASE_URL at
// build time); never run under plain node.

// DEPLOY_BASE is the base with any trailing slash stripped: "/sdk" or "".
export const DEPLOY_BASE = (import.meta.env.BASE_URL || "/").replace(
  /\/+$/,
  "",
);

// withBase prefixes an absolute site path with the deployment base.
// withBase("/rss.xml") → "/sdk/rss.xml" (or "/rss.xml" at root).
export function withBase(path) {
  const p = path.startsWith("/") ? path : `/${path}`;
  return `${DEPLOY_BASE}${p}`;
}
