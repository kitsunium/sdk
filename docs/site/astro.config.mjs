// Astro config — static site for the kitsunium/sdk documentation
// portal. `npm run prebuild` populates src/data/versions.json before
// this file is evaluated, so the redirect target tracks the major
// flagged `default: true` (deprecation ritual = flip the flag — see
// ADR 0007 §5).
import { defineConfig } from "astro/config";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const versionsPath = fileURLToPath(new URL("./src/data/versions.json", import.meta.url));
//: versions.json may be missing on a fresh clone before prebuild;
//: in that case fall back to /v1/ so the build still resolves.
const versions = existsSync(versionsPath)
  ? JSON.parse(readFileSync(versionsPath, "utf8"))
  : [];
const defaultMajor =
  versions.find(v => v?.default)?.major ?? versions[0]?.major ?? "v1";

//: trailing-slash redirects per major: GitHub Pages and most static
//: hosts serve `dist/v1.html` when the URL is `/v1` (no slash) but
//: serve `dist/v1/index.html` when it's `/v1/`. The redirect map
//: rewrites the bare form to the slashed one so links work in both.
const trailingSlashRedirects = Object.fromEntries(
  versions.map(v => [`/${v.major}`, `/${v.major}/`]),
);

export default defineConfig({
  output: "static",
  build: {
    format: "file",
  },
  redirects: {
    //: /  → /<default-major>/  (replaces the previous meta-refresh
    //: pattern; cleaner for SEO + crawlers).
    "/": `/${defaultMajor}/`,
    ...trailingSlashRedirects,
  },
});
