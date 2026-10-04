#!/usr/bin/env node
// docs/site/scripts/gen-symbols.mjs — prebuild step that writes the ⌘K symbol
// index of every release that has docs/api, from docs/api.
//
// docs/api is the SDK's exported API read from the CODE by tools/genindex
// (`make api`, held byte for byte by `make api-check`). sync-versions.mjs has
// just projected each release's docs/api into one page model per package page
// (src/content/api/<release>/<major>/<path>.json — lib/api.mjs apiPages), and
// flagged the release `api` in versions.json. This script turns those models
// into public/_search/symbols-<release>-<major>.json (lib/api.mjs
// symbolEntries): every exported symbol of every package of pkg/<major>/, and
// every method an alias reaches at its owner — the members go/doc, and the
// go/doc index this replaced, cannot see. Each entry links to the record the
// package page renders, so the index and the pages are one projection.
//
// A release cut before docs/api existed has no flag, and no index: its pages
// are its README pages, which Pagefind's full-text search covers.
//
// Node only: no go command, no network. scripts/check-api-counts.mjs holds the
// index to docs/api, counted another way.

import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { blobBase, sourceRef, symbolEntries } from "./lib/api.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
// Astro copies public/ verbatim into dist/, so an index written here is served
// at /_search/symbols-<release>-<major>.json, under the deploy base; the
// search client fetches it on the first keystroke in ⌘K.
const PUBLIC_SEARCH_DIR = join(SITE_ROOT, "public", "_search");
const DATA_DIR = join(SITE_ROOT, "src", "data");
const API_ROOT = join(SITE_ROOT, "src", "content", "api");

/** The shape version of an index; Search.astro refuses any other. */
const INDEX_SCHEMA = 2;

async function readJSON(file, fallback) {
  try {
    return JSON.parse(await readFile(file, "utf8"));
  } catch {
    return fallback;
  }
}

/** Every page model under `dir`, sorted by path. */
async function readModels(dir) {
  const files = [];
  async function walk(d) {
    for (const e of await readdir(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) await walk(p);
      else if (e.name.endsWith(".json")) files.push(p);
    }
  }
  if (existsSync(dir)) await walk(dir);
  files.sort();
  return Promise.all(
    files.map(async (f) => JSON.parse(await readFile(f, "utf8"))),
  );
}

async function main() {
  //: the directory is this script's alone: a stale index of a release that
  //: lost its API — or the go/doc symbols-<major>.json this replaced — would
  //: be copied into dist and served.
  await rm(PUBLIC_SEARCH_DIR, { recursive: true, force: true });
  await mkdir(PUBLIC_SEARCH_DIR, { recursive: true });

  const versions = await readJSON(join(DATA_DIR, "versions.json"), []);
  const build = await readJSON(join(DATA_DIR, "build-info.json"), {});
  if (!Array.isArray(versions) || versions.length === 0) {
    console.warn("[gen-symbols] versions.json empty — skipping");
    return;
  }

  let written = 0;
  for (const v of versions) {
    for (const r of v.releases ?? []) {
      if (!r.api) continue;
      const models = await readModels(join(API_ROOT, r.version, v.major));
      //: a release flagged `api` with no model is a prebuild that wrote half
      //: of what it promised: the index would look empty, not broken.
      if (models.length === 0) {
        throw new Error(
          `${r.version}/${v.major} is flagged api but has no page model`,
        );
      }
      const sourceBase = blobBase(build.repoUrl, sourceRef(build, r.tag));
      const symbols = models.flatMap((m) =>
        symbolEntries(m, { release: r.version, major: v.major, sourceBase }),
      );
      const file = join(
        PUBLIC_SEARCH_DIR,
        `symbols-${r.version}-${v.major}.json`,
      );
      await writeFile(
        file,
        JSON.stringify({
          schema: INDEX_SCHEMA,
          source: "docs/api",
          release: r.version,
          major: v.major,
          module: models[0].module,
          symbols,
        }) + "\n",
      );
      written++;
      console.log(
        `[gen-symbols] wrote ${file} (${symbols.length} symbols: ` +
          `${symbols.filter((s) => !s.via).length} declared, ` +
          `${symbols.filter((s) => s.via).length} reached through an alias)`,
      );
    }
  }
  if (written === 0) {
    console.warn("[gen-symbols] no release has docs/api — no symbol index");
  }
}

main().catch((err) => {
  console.error("[gen-symbols] FAILED:", err);
  process.exit(1);
});
