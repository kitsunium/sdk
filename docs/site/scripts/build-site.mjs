#!/usr/bin/env node
// docs/site/scripts/build-site.mjs — `astro build`, a bounded number of
// releases at a time.
//
// Astro holds a build's whole content store in one process: it loads
// node_modules/.astro/data-store.json, turns it into a JavaScript module and
// bundles that module into the server build (the `astro:data-layer-content`
// virtual module), then imports it again to prerender. Every release adds its
// pages, their rendered HTML and their API models to that store, so the
// build's heap grew with the number of releases — until the deploy of 42
// releases died at Node's default 4 GB limit, in the client build that
// follows the server build (Docs Deploy, 17 October 2026).
//
// A page of a release reads only its own release's entries (the route, the
// sidebar, the breadcrumbs, the search quick links and prev/next filter on
// `<release>/<major>/`), so the releases are built in batches of at most
// DOCS_BATCH_SIZE (default 6), each in its own processes — content sync, then
// `astro build` into a directory of its own, the collections limited to the
// batch by DOCS_BATCH_RELEASES (src/content.config.ts) — and the batches are
// merged into dist/. The heap of a build is then bounded by a batch, not by the
// history.
//
// The merge:
//   - the first batch holds the site's default release and `local`, so the
//     pages that are no release's own (404.html, which suggests the default
//     release's pages) come from it;
//   - every other file a later batch writes and dist/ already has must be
//     byte-identical — the assets are named by their content hash, the
//     redirect pages and public/ are the same in every batch — or the build
//     fails, naming the file;
//   - rss.xml and the sitemap list every page, so their entries are the union
//     of every batch's, in batch order.
// With every release in one batch (DOCS_RELEASES=local, as `make docs-check`
// builds), this is the plain content sync + `astro build` into dist/.

import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { findDefaults, LOCAL_RELEASE } from "./lib/tag-format.mjs";
import {
  batchReleases,
  mergeRss,
  mergeSitemaps,
  SITEMAP_ENTRY_LIMIT,
} from "./lib/batches.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const DIST = join(SITE_ROOT, "dist");
const CONTENT_ROOT = join(SITE_ROOT, "src", "content", "docs");
const VERSIONS_JSON = join(SITE_ROOT, "src", "data", "versions.json");
const STORE_DIR = join(SITE_ROOT, "node_modules", ".astro");
const BATCH_ROOT = join(SITE_ROOT, "node_modules", ".cache", "build-site");
const ASTRO = join(SITE_ROOT, "node_modules", "astro", "astro.js");

const DEFAULT_BATCH_SIZE = 6;

function run(label, args, env) {
  console.log(`[build-site] ${label}: node ${args.map((a) => relative(SITE_ROOT, a) || a).join(" ")}`);
  const r = spawnSync(process.execPath, args, {
    cwd: SITE_ROOT,
    stdio: "inherit",
    env: { ...process.env, ...env },
  });
  if (r.status !== 0) {
    throw new Error(`${label} failed (exit ${r.status ?? r.signal})`);
  }
}

// One batch: a fresh content store holding the batch's releases alone, synced
// and waited for (sync-content.mjs), then astro build into outDir.
function buildBatch(releases, outDir, index, count) {
  const env = releases ? { DOCS_BATCH_RELEASES: releases.join(",") } : {};
  const label = releases
    ? `batch ${index + 1}/${count} (${releases.join(", ")})`
    : "build";
  rmSync(STORE_DIR, { recursive: true, force: true });
  run(`${label}: content sync`, [join(SITE_ROOT, "scripts", "sync-content.mjs")], env);
  run(`${label}: astro build`, [ASTRO, "build", "--outDir", outDir], env);
}

function* walk(dir, base = dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(path, base);
    else yield relative(base, path);
  }
}

const isSitemap = (rel) => /^sitemap-(?:\d+|index)\.xml$/.test(rel);

// Moves a later batch's files into dist/: a file dist/ lacks is moved, one it
// has must be the same bytes. 404.html, rss.xml and the sitemap are left to
// the caller.
function mergeInto(batchDir) {
  let moved = 0;
  for (const rel of walk(batchDir)) {
    if (rel === "404.html" || rel === "rss.xml" || isSitemap(rel)) continue;
    const from = join(batchDir, rel);
    const to = join(DIST, rel);
    if (!existsSync(to)) {
      mkdirSync(dirname(to), { recursive: true });
      renameSync(from, to);
      moved++;
      continue;
    }
    if (statSync(from).size !== statSync(to).size || !readFileSync(from).equals(readFileSync(to))) {
      throw new Error(
        `${rel}: two batches wrote it with different bytes — a page or asset that depends on more than its own release`,
      );
    }
  }
  return moved;
}

function readSitemaps(dir) {
  return readdirSync(dir)
    .filter((f) => /^sitemap-\d+\.xml$/.test(f))
    .sort((a, b) => Number(a.match(/\d+/)[0]) - Number(b.match(/\d+/)[0]))
    .map((f) => readFileSync(join(dir, f), "utf8"));
}

function main() {
  const size = Number(process.env.DOCS_BATCH_SIZE ?? DEFAULT_BATCH_SIZE);
  if (!Number.isInteger(size) || size < 1) {
    throw new Error(`DOCS_BATCH_SIZE=${process.env.DOCS_BATCH_SIZE}: a positive integer`);
  }
  //: the releases are the directories sync-versions materialised; the default
  //: one comes from versions.json, as the 404 page reads it.
  const onDisk = existsSync(CONTENT_ROOT)
    ? readdirSync(CONTENT_ROOT, { withFileTypes: true })
        .filter((e) => e.isDirectory())
        .map((e) => e.name)
    : [];
  const versions = existsSync(VERSIONS_JSON)
    ? JSON.parse(readFileSync(VERSIONS_JSON, "utf8"))
    : [];
  const order = versions.flatMap((v) => v.releases.map((r) => r.version));
  const batches = batchReleases({
    releases: onDisk,
    order,
    first: [findDefaults(versions).release, LOCAL_RELEASE],
    size,
  });

  rmSync(DIST, { recursive: true, force: true });
  if (batches.length <= 1) {
    buildBatch(null, DIST, 0, 1);
    return;
  }

  console.log(
    `[build-site] ${onDisk.length} releases in ${batches.length} batches of at most ${size}`,
  );
  rmSync(BATCH_ROOT, { recursive: true, force: true });
  const rss = [];
  const sitemaps = [];
  let sitemapIndex = null;
  try {
    batches.forEach((releases, i) => {
      const outDir = join(BATCH_ROOT, String(i));
      buildBatch(releases, outDir, i, batches.length);
      if (existsSync(join(outDir, "rss.xml"))) rss.push(readFileSync(join(outDir, "rss.xml"), "utf8"));
      sitemaps.push(...readSitemaps(outDir));
      if (i === 0) {
        if (existsSync(join(outDir, "sitemap-index.xml"))) {
          sitemapIndex = readFileSync(join(outDir, "sitemap-index.xml"), "utf8");
        }
        renameSync(outDir, DIST);
      } else {
        const moved = mergeInto(outDir);
        console.log(`[build-site] batch ${i + 1}/${batches.length}: ${moved} files merged into dist/`);
        rmSync(outDir, { recursive: true, force: true });
      }
    });
  } finally {
    rmSync(BATCH_ROOT, { recursive: true, force: true });
  }

  if (rss.length > 0) writeFileSync(join(DIST, "rss.xml"), mergeRss(rss));
  if (sitemapIndex !== null) {
    for (const f of readdirSync(DIST).filter((f) => /^sitemap-\d+\.xml$/.test(f))) {
      rmSync(join(DIST, f));
    }
    const merged = mergeSitemaps(sitemapIndex, sitemaps, SITEMAP_ENTRY_LIMIT);
    for (const [name, body] of merged) writeFileSync(join(DIST, name), body);
  }
  console.log(`[build-site] ${batches.length} batches merged into ${relative(SITE_ROOT, DIST)}/`);
}

try {
  main();
} catch (err) {
  console.error(`[build-site] ${err.message ?? err}`);
  process.exit(1);
}
