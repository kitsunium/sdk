#!/usr/bin/env node
// docs/site/scripts/sync-versions.mjs — npm prebuild script. Pulls the
// list of released tags from `gh release list`, picks the latest per
// major, materialises src/content/docs/<major>/ from each tag's tree
// using `git worktree`, and writes src/data/versions.json (consumed by
// VersionDropdown).
//
// Idempotent: `git worktree prune` on entry, try/finally `worktree
// remove --force` per major so a killed run never blocks the next
// one. Every shell call uses execFile (no shell, no injection).

import { execFile as _execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdir, writeFile, readdir, copyFile, stat } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { isValidTag, buildVersionsJson, parseTag } from "./lib/tag-format.mjs";

const execFile = promisify(_execFile);
const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const REPO_ROOT = resolve(SITE_ROOT, "..", "..");
const CONTENT_ROOT = join(SITE_ROOT, "src", "content", "docs");
const VERSIONS_JSON = join(SITE_ROOT, "src", "data", "versions.json");

async function shellSafe(file, args, opts = {}) {
  // execFile is shell-less; arguments cannot be interpreted as
  // metacharacters even if a tag name slips through the regex gate.
  try {
    const { stdout } = await execFile(file, args, { cwd: REPO_ROOT, ...opts });
    return stdout;
  } catch (err) {
    return { __error: err.message ?? String(err), stdout: err.stdout ?? "" };
  }
}

async function listReleasesViaGh() {
  const out = await shellSafe("gh", [
    "release", "list",
    "--limit", "200",
    "--json", "tagName,publishedAt,isDraft,isPrerelease",
  ]);
  if (typeof out !== "string") {
    console.warn("[sync-versions] gh release list unavailable — falling back to local tags");
    return null;
  }
  return JSON.parse(out);
}

async function listReleasesViaGitTags() {
  const out = await shellSafe("git", ["tag", "-l", "pkg/v*/v*"]);
  if (typeof out !== "string") return [];
  return out.split("\n")
    .filter(Boolean)
    .filter(isValidTag)
    .map(t => ({ tagName: t, publishedAt: null, isDraft: false, isPrerelease: false }));
}

async function copyTree(src, dest, predicate) {
  if (!existsSync(src)) return 0;
  await mkdir(dest, { recursive: true });
  let count = 0;
  for (const entry of await readdir(src, { withFileTypes: true })) {
    const s = join(src, entry.name);
    const d = join(dest, entry.name);
    if (entry.isDirectory()) {
      count += await copyTree(s, d, predicate);
    } else if (entry.isFile() && predicate(entry.name)) {
      await copyFile(s, d);
      count++;
    }
  }
  return count;
}

async function materialiseMajor(major, tag) {
  const dest = join(CONTENT_ROOT, major);
  await mkdir(dest, { recursive: true });
  const wt = `/tmp/wt-${major}-${Date.now()}`;
  try {
    await shellSafe("git", ["worktree", "prune"]);
    const add = await shellSafe("git", ["worktree", "add", "--detach", wt, tag]);
    if (typeof add !== "string") {
      console.warn(`[sync-versions] worktree add failed for ${tag}; skipping`);
      return;
    }
    const pkgRoot = join(wt, "pkg", major);
    const adrRoot = join(wt, "docs", "adr");
    const isDoc = name => /\.md$/i.test(name);
    const pkgCount = await copyTree(pkgRoot, dest, isDoc);
    const adrCount = await copyTree(adrRoot, join(dest, "adr"), isDoc);
    console.log(`[sync-versions] ${major} <- ${tag}: ${pkgCount} pkg doc(s) + ${adrCount} ADR(s)`);
  } finally {
    await shellSafe("git", ["worktree", "remove", "--force", wt]);
  }
}

async function ensureBootstrapEntry() {
  // No releases yet → synthesise a single v1 entry pointing at HEAD so
  // the docs build still produces a working /v1/ subtree on day one.
  console.log("[sync-versions] bootstrap mode — no releases found, using HEAD as v1");
  const dest = join(CONTENT_ROOT, "v1");
  await mkdir(dest, { recursive: true });
  await copyTree(join(REPO_ROOT, "pkg", "v1"), dest, n => /\.md$/i.test(n));
  await copyTree(join(REPO_ROOT, "docs", "adr"), join(dest, "adr"), n => /\.md$/i.test(n));
  return [{ major: "v1", latest: "0.0.0", default: true, eol: false, publishedAt: null }];
}

async function main() {
  await mkdir(dirname(VERSIONS_JSON), { recursive: true });

  let releases = await listReleasesViaGh();
  if (!releases) releases = await listReleasesViaGitTags();

  let versions;
  if (!releases || releases.length === 0) {
    versions = await ensureBootstrapEntry();
  } else {
    versions = buildVersionsJson(releases);
    // Materialise each non-EOL major from its latest tag.
    for (const v of versions) {
      const tag = `pkg/${v.major}/v${v.latest}`;
      if (!isValidTag(tag)) continue;
      await materialiseMajor(v.major, tag);
    }
  }

  await writeFile(VERSIONS_JSON, JSON.stringify(versions, null, 2) + "\n");
  console.log(`[sync-versions] wrote ${VERSIONS_JSON} (${versions.length} majors)`);
}

main().catch(err => {
  console.error("[sync-versions] FAILED:", err);
  process.exit(1);
});
