// Astro config — static site for the kitsunium/sdk documentation
// portal. `npm run prebuild` populates src/data/versions.json before
// this file is evaluated, so the redirect target tracks the major
// flagged `default: true` (deprecation ritual = flip the flag — see
// ADR 0007 §5).
//
// build.format = "directory" so the catch-all dynamic route at
// src/pages/[major]/[...slug].astro generates dist/v1/index.html for
// the v1 landing page (slug=undefined). With format:"file" the same
// route would emit dist/v1.html and clash with a redirect map,
// producing an infinite redirect loop.
import { defineConfig } from "astro/config";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const versionsPath = fileURLToPath(
  new URL("./src/data/versions.json", import.meta.url),
);
//: versions.json may be missing on a fresh clone before prebuild;
//: in that case fall back to /v1/ so the build still resolves.
const versions = existsSync(versionsPath)
  ? JSON.parse(readFileSync(versionsPath, "utf8"))
  : [];
const defaultMajor =
  versions.find((v) => v?.default)?.major ?? versions[0]?.major ?? "v1";

export default defineConfig({
  output: "static",
  build: {
    //: directory format = clean URLs ("/v1/", "/getting-started/") and
    //: index.html-per-route so the catch-all rest param produces a real
    //: landing page for each major.
    format: "directory",
  },
  redirects: {
    //: /  →  /<default-major>/  (replaces the previous meta-refresh).
    //: The trailing-slash variants are handled by `format: "directory"`
    //: which redirects /v1 → /v1/ automatically; no explicit entries
    //: needed for that (and adding them would re-create the loop).
    "/": `/${defaultMajor}/`,
  },
});
