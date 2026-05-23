#!/usr/bin/env node
// docs/site/scripts/gen-symbols.mjs — prebuild step that delegates to
// the Go program at tools/genindex. Reads pkg/v1's AST (via go/doc)
// and writes a per-major search index to src/data/symbols-<major>.json
// for each major flagged in versions.json.
//
// Runs strictly stdlib-only on both sides: Node here just spawns Go.
// GOWORK=off because tools/genindex lives outside go.work (it's a
// build tool, not a library — keeps the 5-module invariant from
// /workspace/CLAUDE.md intact).

import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const REPO_ROOT = resolve(SITE_ROOT, "..", "..");
const TOOL_DIR = join(REPO_ROOT, "tools", "genindex");
// Astro copies public/ verbatim into dist/, so writing symbols here
// makes them available at /_search/symbols-<major>.json at runtime
// (the Search component fetches that URL on first ⌘K keystroke).
const PUBLIC_SEARCH_DIR = join(SITE_ROOT, "public", "_search");
const DATA_DIR = join(SITE_ROOT, "src", "data");

async function loadVersions() {
  try {
    const txt = await readFile(join(DATA_DIR, "versions.json"), "utf8");
    return JSON.parse(txt);
  } catch {
    return [];
  }
}

function runGenindex({
  inputDir,
  outputFile,
  modulePath,
  urlBase,
  sourceURLPrefix,
}) {
  const args = [
    "run",
    ".",
    "-input",
    inputDir,
    "-output",
    outputFile,
    "-module",
    modulePath,
    "-url-base",
    urlBase,
    "-repo-root",
    REPO_ROOT,
  ];
  if (sourceURLPrefix) {
    args.push("-source-url-prefix", sourceURLPrefix);
  }
  const result = spawnSync("go", args, {
    cwd: TOOL_DIR,
    env: { ...process.env, GOWORK: "off" },
    stdio: ["ignore", "inherit", "inherit"],
  });
  if (result.status !== 0) {
    console.error(`[gen-symbols] genindex failed for ${modulePath}`);
    process.exit(result.status ?? 1);
  }
}

async function loadBuildInfo() {
  try {
    const txt = await readFile(join(DATA_DIR, "build-info.json"), "utf8");
    return JSON.parse(txt);
  } catch {
    return {};
  }
}

//: Build the "https://github.com/<org>/<repo>/blob/<ref>/" prefix the
//: Go tool prepends to repo-relative source paths. Prefer the branch
//: (lets readers follow follow-up commits) but fall back to the exact
//: commit SHA so the deep link still resolves on stale builds.
function deriveSourceURLPrefix(build) {
  if (!build?.repoUrl) return "";
  const ref =
    build.branch && build.branch !== "HEAD"
      ? build.branch
      : (build.commitFull ?? "main");
  return `${build.repoUrl.replace(/\/$/, "")}/blob/${ref}`;
}

async function main() {
  if (!existsSync(TOOL_DIR)) {
    console.warn(`[gen-symbols] ${TOOL_DIR} missing — skipping`);
    return;
  }
  mkdirSync(PUBLIC_SEARCH_DIR, { recursive: true });
  mkdirSync(DATA_DIR, { recursive: true });

  const versions = await loadVersions();
  if (!Array.isArray(versions) || versions.length === 0) {
    console.warn("[gen-symbols] versions.json empty — skipping");
    return;
  }
  const buildInfo = await loadBuildInfo();
  const sourceURLPrefix = deriveSourceURLPrefix(buildInfo);

  for (const v of versions) {
    //: every major maps to pkg/<major>/ on disk. Releases share that
    //: package tree until a per-tag worktree pipeline lands (today
    //: only "local" exists per major).
    const inputDir = join(REPO_ROOT, "pkg", v.major);
    if (!existsSync(inputDir)) {
      console.warn(`[gen-symbols] ${inputDir} missing — skipping ${v.major}`);
      continue;
    }
    const release =
      v.releases?.find((r) => r.default)?.version ??
      v.releases?.[0]?.version ??
      "local";
    //: URL base is /<release>/<major> — mirrors the route layout
    //: (src/pages/[release]/[major]/...). Symbol anchors will link to
    //: /local/v1/codec/#Marshal, not /v1/local/codec/#Marshal.
    const urlBase = `/${release}/${v.major}`;
    const modulePath = `github.com/kitsunium/sdk/pkg/${v.major}`;
    const outputFile = join(PUBLIC_SEARCH_DIR, `symbols-${v.major}.json`);
    runGenindex({
      inputDir,
      outputFile,
      modulePath,
      urlBase,
      sourceURLPrefix,
    });
  }
}

main().catch((err) => {
  console.error("[gen-symbols] FAILED:", err);
  process.exit(1);
});
