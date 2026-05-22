// Astro config — static site for the kitsunium/sdk documentation.
// `npm run prebuild` writes src/data/versions.json before this file
// is evaluated; that file drives the redirect map below so the root
// of the site always lands on the newest non-EOL major's default
// release (or "local" while no real tag exists).
import { defineConfig } from "astro/config";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const versionsPath = fileURLToPath(
  new URL("./src/data/versions.json", import.meta.url),
);
const versions = existsSync(versionsPath)
  ? JSON.parse(readFileSync(versionsPath, "utf8"))
  : [];

const defaultMajor =
  versions.find((v) => v?.default)?.major ?? versions[0]?.major ?? "v1";
const defaultMajorEntry =
  versions.find((v) => v.major === defaultMajor) ?? versions[0];
const defaultRelease =
  defaultMajorEntry?.releases?.find((r) => r.default)?.version ??
  defaultMajorEntry?.releases?.[0]?.version ??
  "local";

//: Per-major bare-path redirect: hitting /v1 (no release segment) sends
//: the user to /v1/<default-release-of-v1>/. Computed at build time so
//: a future v2 with its own default release flips automatically.
const perMajorRedirects = Object.fromEntries(
  versions.flatMap((v) => {
    const r = v.releases?.find((x) => x.default) ?? v.releases?.[0];
    if (!r) return [];
    return [[`/${v.major}`, `/${v.major}/${r.version}/`]];
  }),
);

export default defineConfig({
  output: "static",
  build: {
    //: directory format so the catch-all generates real index.html files
    //: per route (avoids the redirect-loop trap with file format).
    format: "directory",
  },
  redirects: {
    "/": `/${defaultMajor}/${defaultRelease}/`,
    ...perMajorRedirects,
  },
});
