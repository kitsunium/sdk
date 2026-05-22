#!/usr/bin/env node
// docs/site/scripts/sync-versions.mjs — npm prebuild script.
//
// Materialises every (major, release) coordinate into
// src/content/docs/<major>/<release>/*.md from the REAL source files
// of the corresponding tag (or HEAD in bootstrap mode). The docs site
// never carries hand-authored prose — every page in the versioned
// tree is a copy of a markdown file that lives in pkg/<major>/**,
// docs/adr/*.md, or a section of /workspace/CLAUDE.md.
//
// Single source of truth = the real code repository. The docs site is
// a renderer.

import { execFile as _execFile } from "node:child_process";
import { promisify } from "node:util";
import {
  mkdir,
  writeFile,
  readdir,
  copyFile,
  readFile,
} from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, resolve, basename } from "node:path";
import { fileURLToPath } from "node:url";

import {
  isValidTag,
  buildVersionsJson,
  localOnlyVersions,
  LOCAL_RELEASE,
} from "./lib/tag-format.mjs";

const execFile = promisify(_execFile);
const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const REPO_ROOT = resolve(SITE_ROOT, "..", "..");
const CONTENT_ROOT = join(SITE_ROOT, "src", "content", "docs");
const VERSIONS_JSON = join(SITE_ROOT, "src", "data", "versions.json");

async function shellSafe(file, args, opts = {}) {
  try {
    const { stdout } = await execFile(file, args, { cwd: REPO_ROOT, ...opts });
    return stdout;
  } catch (err) {
    return { __error: err.message ?? String(err), stdout: err.stdout ?? "" };
  }
}

async function listReleasesViaGh() {
  const out = await shellSafe("gh", [
    "release",
    "list",
    "--limit",
    "200",
    "--json",
    "tagName,publishedAt,isDraft,isPrerelease",
  ]);
  if (typeof out !== "string") {
    console.warn(
      "[sync-versions] gh release list unavailable — falling back to local tags",
    );
    return null;
  }
  return JSON.parse(out);
}

async function listReleasesViaGitTags() {
  const out = await shellSafe("git", ["tag", "-l", "pkg/v*/v*"]);
  if (typeof out !== "string") return [];
  return out
    .split("\n")
    .filter(Boolean)
    .filter(isValidTag)
    .map((t) => ({
      tagName: t,
      publishedAt: null,
      isDraft: false,
      isPrerelease: false,
    }));
}

async function discoverMajors(root) {
  const pkgRoot = join(root, "pkg");
  if (!existsSync(pkgRoot)) return [];
  const entries = await readdir(pkgRoot, { withFileTypes: true });
  return entries
    .filter((e) => e.isDirectory() && /^v\d+$/.test(e.name))
    .map((e) => e.name)
    .sort((a, b) => Number(a.slice(1)) - Number(b.slice(1)));
}

// ─── Section extraction from /workspace/CLAUDE.md ──────────────────
// The cross-version philosophy / architecture / how-to-work content
// lives in the root CLAUDE.md. We slice it by H2 (## …) so each
// section ships as its own page.

async function extractClaudeMdSections(claudeMdPath) {
  if (!existsSync(claudeMdPath)) return {};
  const body = await readFile(claudeMdPath, "utf8");
  const sections = {};
  const lines = body.split("\n");
  let title = null;
  let buf = [];
  const flush = () => {
    if (title) sections[title] = buf.join("\n").trim();
    buf = [];
  };
  for (const line of lines) {
    const m = line.match(/^##\s+(.+?)\s*$/);
    if (m) {
      flush();
      title = m[1].trim();
    } else if (title) {
      buf.push(line);
    }
  }
  flush();
  return sections;
}

function frontmatter(title, description) {
  const desc = description
    ? `\ndescription: ${JSON.stringify(description)}`
    : "";
  return `---\ntitle: ${JSON.stringify(title)}${desc}\n---\n\n`;
}

// ─── Per-release materialiser ─────────────────────────────────────
// Materialises one (major, release) under src/content/docs/<m>/<r>/
// from a source root (either /workspace for "local" or a git worktree
// checkout for a tag). Every page is extracted from a real source file
// — nothing is invented.

async function materialiseRelease(major, release, sourceRoot) {
  const dest = join(CONTENT_ROOT, major, release);
  await mkdir(dest, { recursive: true });

  const pkgMajor = join(sourceRoot, "pkg", major);
  const claudeMd = join(sourceRoot, "CLAUDE.md");
  const adrDir = join(sourceRoot, "docs", "adr");

  // 1. Landing page (index.md): the package's CLAUDE.md is the most
  // authoritative "what is pkg/<major>" doc.
  const landing = join(pkgMajor, "CLAUDE.md");
  if (existsSync(landing)) {
    await copyFile(landing, join(dest, "index.md"));
  } else {
    await writeFile(
      join(dest, "index.md"),
      frontmatter(major, `Documentation snapshot for ${major}`) +
        `# ${major}\n\nDocumentation snapshot for ${major}.\n`,
    );
  }

  // 2. Service READMEs. Each sub-package under pkg/<major>/ that
  // ships a README.md becomes a top-level service page.
  if (existsSync(pkgMajor)) {
    const subs = await readdir(pkgMajor, { withFileTypes: true });
    for (const sub of subs) {
      if (!sub.isDirectory()) continue;
      const readme = join(pkgMajor, sub.name, "README.md");
      if (existsSync(readme)) {
        await copyFile(readme, join(dest, `${sub.name}.md`));
      }
    }
    // Special case: codec benchmark report -> /v?/<r>/benchmarks/
    const bench = join(pkgMajor, "codec", "BENCH.md");
    if (existsSync(bench)) {
      await copyFile(bench, join(dest, "benchmarks.md"));
    }
  }

  // 3. ADRs — cross-version, snapshotted per release so the version
  // dropdown shows the ADR set as of that tag.
  if (existsSync(adrDir)) {
    const adrDest = join(dest, "adr");
    await mkdir(adrDest, { recursive: true });
    const adrIndexRows = [];
    for (const f of await readdir(adrDir)) {
      if (!/\.md$/i.test(f)) continue;
      await copyFile(join(adrDir, f), join(adrDest, f));
      adrIndexRows.push(
        `- [${f.replace(/\.md$/, "")}](./${f.replace(/\.md$/, "")}/)`,
      );
    }
    // Synthesised index lists every ADR file copied above.
    const adrIndex =
      frontmatter(
        "Architecture Decision Records",
        `Frozen ADR set as of ${release === LOCAL_RELEASE ? "HEAD" : release}`,
      ) +
      `# ADRs\n\n` +
      `Architecture Decision Records snapshotted at \`${release}\`.\n\n` +
      adrIndexRows.sort().join("\n") +
      "\n";
    await writeFile(join(adrDest, "index.md"), adrIndex);
  }

  // 4. Cross-cutting pages extracted from /workspace/CLAUDE.md
  // sections. Maintains "TOUT dynamique" — these are real chunks of
  // the canonical CLAUDE.md, not hand-authored prose.
  const sections = await extractClaudeMdSections(claudeMd);
  const pageMap = [
    ["philosophy", "SDK-wide rules (non-negotiable)", "SDK philosophy"],
    ["architecture", "Architecture at a glance", "Architecture at a glance"],
    ["getting-started", "How to work", "Getting started"],
    ["verification", "Verification", "Verification matrix"],
  ];
  for (const [slug, sectionTitle, pageTitle] of pageMap) {
    const body = sections[sectionTitle];
    if (!body) continue;
    await writeFile(
      join(dest, `${slug}.md`),
      frontmatter(pageTitle, sectionTitle) + `# ${pageTitle}\n\n` + body + "\n",
    );
  }
}

async function materialiseLocal(major) {
  console.log(`[sync-versions] ${major}/local <- HEAD`);
  await materialiseRelease(major, LOCAL_RELEASE, REPO_ROOT);
}

async function materialiseTag(major, version, tag) {
  const wt = `/tmp/wt-${major}-${version}-${Date.now()}`;
  try {
    await shellSafe("git", ["worktree", "prune"]);
    const add = await shellSafe("git", [
      "worktree",
      "add",
      "--detach",
      wt,
      tag,
    ]);
    if (typeof add !== "string") {
      console.warn(`[sync-versions] worktree add failed for ${tag}; skipping`);
      return;
    }
    console.log(`[sync-versions] ${major}/${version} <- ${tag}`);
    await materialiseRelease(major, version, wt);
  } finally {
    await shellSafe("git", ["worktree", "remove", "--force", wt]);
  }
}

async function main() {
  await mkdir(dirname(VERSIONS_JSON), { recursive: true });

  // 1. Pick up all real release tags via gh, fallback to git tag -l.
  let releases = await listReleasesViaGh();
  if (!releases) releases = await listReleasesViaGitTags();

  const realByMajor =
    releases && releases.length > 0 ? buildVersionsJson(releases) : [];
  const majorsOnDisk = await discoverMajors(REPO_ROOT);

  // 2. Stitch the two: every major on disk gets a "local" release;
  // any major with real tags also gets those tags. The newest
  // (default) release of the newest major is the site default.
  const merged = new Map();
  for (const m of majorsOnDisk) {
    merged.set(m, { major: m, default: false, eol: false, releases: [] });
  }
  for (const entry of realByMajor) {
    if (!merged.has(entry.major)) {
      merged.set(entry.major, {
        major: entry.major,
        default: false,
        eol: false,
        releases: [],
      });
    }
    const slot = merged.get(entry.major);
    slot.releases.push(...entry.releases);
  }
  // Always prepend a "local" release for the major(s) that exist on disk.
  for (const m of majorsOnDisk) {
    const slot = merged.get(m);
    slot.releases.unshift({
      version: LOCAL_RELEASE,
      tag: null,
      label: LOCAL_RELEASE,
      default: slot.releases.length === 0,
      publishedAt: null,
    });
  }

  const versions = [...merged.values()].sort(
    (a, b) => Number(a.major.slice(1)) - Number(b.major.slice(1)),
  );

  // Newest non-EOL major wins the default flag.
  if (versions.length > 0) {
    versions.forEach((v) => {
      v.default = false;
    });
    const newest = versions.filter((v) => !v.eol).at(-1);
    if (newest) newest.default = true;
  }

  // 3. Materialise content for every (major, release) combo.
  for (const v of versions) {
    for (const r of v.releases) {
      if (r.version === LOCAL_RELEASE) {
        await materialiseLocal(v.major);
      } else if (r.tag) {
        await materialiseTag(v.major, r.version, r.tag);
      }
    }
  }

  await writeFile(VERSIONS_JSON, JSON.stringify(versions, null, 2) + "\n");
  console.log(
    `[sync-versions] wrote ${VERSIONS_JSON} (${versions.length} major(s), ` +
      `${versions.reduce((n, v) => n + v.releases.length, 0)} release(s))`,
  );
}

main().catch((err) => {
  console.error("[sync-versions] FAILED:", err);
  process.exit(1);
});
