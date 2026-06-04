#!/usr/bin/env node
// gen-features.mjs — fast, local-release-only regeneration of the curated
// feature-catalog banner data (src/data/features-<local>-<major>.json).
//
// This is a dev / verification convenience: the full docs build
// (sync-versions.mjs) produces the same data for every (release, major) at
// `npm run prebuild`. Unlike the old gen-whats-new.mjs this is NOT wired to a
// git hook — provenance comes from stable past anchors, so the catalog only
// changes when the registry (src/data/features.mjs) or an anchor's history
// does, which the prebuild generation at deploy time already captures.

import { writeFile, mkdir, readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { LOCAL_RELEASE } from "./lib/tag-format.mjs";
import {
  deriveProvenance,
  buildFeaturePayload,
  uniqueAnchors,
} from "./lib/features.mjs";
import registry from "../src/data/features.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const REPO_ROOT = resolve(SITE_ROOT, "..", "..");
const DATA_DIR = join(SITE_ROOT, "src", "data");
const REPO_URL = "https://github.com/kitsunium/sdk";

// majorsInRepo lists pkg/v<N>/ majors so a future v2 gets a catalog too;
// falls back to v1 when pkg/ is absent (partial checkout).
async function majorsInRepo() {
  const pkgRoot = join(REPO_ROOT, "pkg");
  if (!existsSync(pkgRoot)) return ["v1"];
  const entries = await readdir(pkgRoot, { withFileTypes: true });
  const majors = entries
    .filter((e) => e.isDirectory() && /^v\d+$/.test(e.name))
    .map((e) => e.name);
  return majors.length > 0 ? majors : ["v1"];
}

async function main() {
  //: derive provenance once per distinct anchor (at HEAD = the local release).
  const provByAnchor = {};
  for (const anchor of uniqueAnchors(registry)) {
    provByAnchor[anchor] = await deriveProvenance(REPO_ROOT, anchor, "HEAD");
  }

  await mkdir(DATA_DIR, { recursive: true });
  for (const major of await majorsInRepo()) {
    const payload = buildFeaturePayload(registry, {
      release: LOCAL_RELEASE,
      major,
      repoUrl: REPO_URL,
      provByAnchor,
    });
    const outPath = join(DATA_DIR, `features-${LOCAL_RELEASE}-${major}.json`);
    await writeFile(outPath, JSON.stringify(payload, null, 2) + "\n");
    process.stdout.write(
      `features: wrote ${outPath} (${payload.recent.length} recent, ${payload.domains.length} domains)\n`,
    );
  }
}

await main();
