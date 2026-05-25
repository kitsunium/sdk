// gen-whats-new.mjs — fast, gh-free regeneration of the "What's new" banner
// data, used by the pre-commit hook (scripts/pre-commit/gen-whats-new.sh).
//
// Unlike sync-versions.mjs (full docs build: queries `gh release list`,
// rewrites the entire content tree) this reads ONLY `git log` and rewrites
// src/data/whats-new-<local>-<major>.json — safe and quick to run on every
// commit. The selection logic is shared via lib/whats-new.mjs, so the hook
// and the full build can never diverge.
//
// Scope: the local (untagged) release only — tagged-release snapshots are
// frozen at tag time and are not a per-commit concern.

import { writeFile, readFile, mkdir, readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { LOCAL_RELEASE } from "./lib/tag-format.mjs";
import { gitLogCommits, buildWhatsNewPayload } from "./lib/whats-new.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const REPO_ROOT = resolve(SITE_ROOT, "..", "..");
const DATA_DIR = join(SITE_ROOT, "src", "data");
const REPO_URL = "https://github.com/kitsunium/sdk";

// LOCAL_WINDOW mirrors sync-versions' local changelog window (last 30 commits
// of HEAD) so the banner this hook writes matches what a full build produces.
const LOCAL_WINDOW = ["-n", "30", "HEAD"];

// majorsInRepo lists the pkg/v<N>/ majors so a future v2 gets a banner too;
// falls back to v1 when pkg/ is absent (e.g. a partial checkout).
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
  const commits = await gitLogCommits(REPO_ROOT, LOCAL_WINDOW);
  await mkdir(DATA_DIR, { recursive: true });
  for (const major of await majorsInRepo()) {
    const payload = buildWhatsNewPayload(commits, {
      release: LOCAL_RELEASE,
      major,
      repoUrl: REPO_URL,
    });
    const outPath = join(DATA_DIR, `whats-new-${LOCAL_RELEASE}-${major}.json`);
    //: Idempotent on unchanged entries: skip the write (and the generatedAt
    //: bump) when the banner content is identical, so a commit that adds no
    //: feat/fix never touches this file. Only the entries matter — the
    //: timestamp alone is not worth a per-commit diff.
    let prevEntries = null;
    try {
      prevEntries = JSON.parse(await readFile(outPath, "utf8")).entries;
    } catch {
      //: missing or unreadable file → treat as "changed", write fresh.
      prevEntries = null;
    }
    if (JSON.stringify(prevEntries) === JSON.stringify(payload.entries)) {
      continue;
    }
    await writeFile(outPath, JSON.stringify(payload, null, 2) + "\n");
  }
}

await main();
