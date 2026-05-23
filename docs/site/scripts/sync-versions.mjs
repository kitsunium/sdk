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
import { RESERVED } from "./lib/page-catalog.mjs";

const execFile = promisify(_execFile);
const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");
const REPO_ROOT = resolve(SITE_ROOT, "..", "..");
const CONTENT_ROOT = join(SITE_ROOT, "src", "content", "docs");
const VERSIONS_JSON = join(SITE_ROOT, "src", "data", "versions.json");
const BUILD_INFO_JSON = join(SITE_ROOT, "src", "data", "build-info.json");

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

/**
 * Remove gomarkdoc's auto-generated "## Index" block (the flat
 * symbol-list reference at the top of every README). It's pure
 * duplication of the right-side TOC the docs site renders from
 * the same headings — keeping it would have every page show its
 * symbol map twice.
 *
 * Matches "## Index" up to (but not including) the next H2 ("\n## ")
 * or end of file. The strip is markdown-safe because gomarkdoc
 * always emits the Index as a single H2 section followed by
 * sibling H2s (Constants / Variables / per-symbol).
 *
 * @param {string} body  — full markdown content
 * @returns {string}     — same content with the Index section removed
 */
function stripGomarkdocIndex(body) {
  return body.replace(/\n## Index\n[\s\S]*?(?=\n## |\s*$)/, "\n");
}

/**
 * Build a minimal YAML frontmatter block. Every field is optional —
 * omitted fields are simply not emitted (Astro's `.passthrough()`
 * schema lets missing fields stay undefined in entry.data).
 *
 * `source` is the repo-relative path to the original file the page
 * was materialised from. The site's EditLink component uses it to
 * deep-link readers back to the source for an "Edit this page on
 * GitHub" affordance.
 *
 * @param {{title?: string|null, description?: string|null, source?: string|null}} opts
 * @returns {string}
 */
function frontmatter({ title, description, source } = {}) {
  const parts = [];
  if (title) parts.push(`title: ${JSON.stringify(title)}`);
  if (description) parts.push(`description: ${JSON.stringify(description)}`);
  if (source) parts.push(`source: ${JSON.stringify(source)}`);
  if (parts.length === 0) return "";
  return `---\n${parts.join("\n")}\n---\n\n`;
}

// ─── Per-release materialiser ─────────────────────────────────────
// Materialises one (major, release) under src/content/docs/<m>/<r>/
// from a source root (either /workspace for "local" or a git worktree
// checkout for a tag). Every page is extracted from a real source file
// — nothing is invented.

async function materialiseRelease(major, release, sourceRoot) {
  //: Dest is <release>/<major>/ — release is the time axis (snapshot
  //: in git, e.g. local / v0.1.0), major is the API-surface axis
  //: (v1 / v2). The hierarchy reads as "this release's v1 surface",
  //: which matches how readers reason about a versioned SDK.
  const dest = join(CONTENT_ROOT, release, major);
  await mkdir(dest, { recursive: true });

  const pkgMajor = join(sourceRoot, "pkg", major);
  const claudeMd = join(sourceRoot, "CLAUDE.md");
  const adrDir = join(sourceRoot, "docs", "adr");

  // 1. Landing page (index.md): the package's CLAUDE.md is the
  // authoritative "what is pkg/<major>" doc. We prepend a frontmatter
  // `title` from page-catalog.RESERVED.index.label so the page-header
  // h1 in the docs site matches the sidebar label (single source of
  // truth — RESERVED is shared with Sidebar + Search).
  const landingTitle = RESERVED.index.label ?? major;
  const landing = join(pkgMajor, "CLAUDE.md");
  if (existsSync(landing)) {
    const body = await readFile(landing, "utf8");
    await writeFile(
      join(dest, "index.md"),
      frontmatter({
        title: landingTitle,
        description: `Overview of pkg/${major}`,
        source: `pkg/${major}/CLAUDE.md`,
      }) + body,
    );
  } else {
    await writeFile(
      join(dest, "index.md"),
      frontmatter({
        title: landingTitle,
        description: `Documentation snapshot for ${major}`,
      }) + `# ${major}\n\nDocumentation snapshot for ${major}.\n`,
    );
  }

  // 2. Service READMEs. Each sub-package under pkg/<major>/ that
  // ships a README.md becomes a top-level service page. We post-
  // process the gomarkdoc output to strip the "## Index" block —
  // it's a redundant flat list of every symbol that duplicates
  // the right-side TOC the docs site already renders. We inject a
  // `source` frontmatter pointing at the gomarkdoc-generated README
  // so the EditLink can deep-link readers to the file that drives
  // the page (their edits should target the Go doc comments — but
  // pkg.go.dev shows the right entry point all the same).
  if (existsSync(pkgMajor)) {
    const subs = await readdir(pkgMajor, { withFileTypes: true });
    for (const sub of subs) {
      if (!sub.isDirectory()) continue;
      const readme = join(pkgMajor, sub.name, "README.md");
      if (existsSync(readme)) {
        const raw = await readFile(readme, "utf8");
        await writeFile(
          join(dest, `${sub.name}.md`),
          frontmatter({ source: `pkg/${major}/${sub.name}/README.md` }) +
            stripGomarkdocIndex(raw),
        );
      }
    }
    // Special case: codec benchmark report -> /v?/<r>/benchmarks/
    // Prepend frontmatter so the page-header h1 reads "Benchmarks"
    // (RESERVED label) — aligned with the sidebar entry. BENCH.md's
    // own '# Benchmarks — pkg/v1/codec' h1 still renders inside the
    // article body for context.
    const bench = join(pkgMajor, "codec", "BENCH.md");
    if (existsSync(bench)) {
      const benchTitle = RESERVED.benchmarks.label ?? "Benchmarks";
      const benchBody = await readFile(bench, "utf8");
      await writeFile(
        join(dest, "benchmarks.md"),
        frontmatter({
          title: benchTitle,
          description: `Codec benchmark report`,
          source: `pkg/${major}/codec/BENCH.md`,
        }) + benchBody,
      );
    }
  }

  // 3. ADRs — cross-version, snapshotted per release so the version
  // dropdown shows the ADR set as of that tag. We read+rewrite each
  // file (instead of plain copyFile) so a `source` frontmatter can
  // be prepended for the EditLink.
  if (existsSync(adrDir)) {
    const adrDest = join(dest, "adr");
    await mkdir(adrDest, { recursive: true });
    const adrIndexRows = [];
    for (const f of await readdir(adrDir)) {
      if (!/\.md$/i.test(f)) continue;
      const raw = await readFile(join(adrDir, f), "utf8");
      await writeFile(
        join(adrDest, f),
        frontmatter({ source: `docs/adr/${f}` }) + raw,
      );
      adrIndexRows.push(
        `- [${f.replace(/\.md$/, "")}](./${f.replace(/\.md$/, "")}/)`,
      );
    }
    // Synthesised index lists every ADR file copied above. Title comes
    // from RESERVED so sidebar ("ADRs") and page-header h1 match.
    const adrTitle = RESERVED.adr.label ?? "ADRs";
    const adrIndex =
      frontmatter({
        title: adrTitle,
        description: `Frozen ADR set as of ${release === LOCAL_RELEASE ? "HEAD" : release}`,
      }) +
      `# ${adrTitle}\n\n` +
      `Architecture Decision Records snapshotted at \`${release}\`.\n\n` +
      adrIndexRows.sort().join("\n") +
      "\n";
    await writeFile(join(adrDest, "index.md"), adrIndex);
  }

  // 4. Cross-cutting pages extracted from /workspace/CLAUDE.md
  // sections. Title = RESERVED label so sidebar and page-header h1
  // are byte-identical; description = the source section name
  // (preserved for SEO + the modal subtitle). Source points back to
  // the root CLAUDE.md so the EditLink works (readers will land on
  // the file even if the section anchor isn't preserved).
  const sections = await extractClaudeMdSections(claudeMd);
  const pageMap = [
    ["philosophy", "SDK-wide rules (non-negotiable)"],
    ["architecture", "Architecture at a glance"],
    ["getting-started", "How to work"],
    ["verification", "Verification"],
  ];
  for (const [slug, sectionTitle] of pageMap) {
    const body = sections[sectionTitle];
    if (!body) continue;
    const pageTitle = RESERVED[slug]?.label ?? sectionTitle;
    await writeFile(
      join(dest, `${slug}.md`),
      frontmatter({
        title: pageTitle,
        description: sectionTitle,
        source: `CLAUDE.md`,
      }) +
        `# ${pageTitle}\n\n` +
        body +
        "\n",
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

  // 4. Capture build metadata for the Header. Surfaces the real git
  // coordinate a deployed site was built from (commit SHA, date,
  // branch, dirty flag) — a "pro" build provenance band, GitHub-style.
  const commitShort = await shellSafe("git", ["rev-parse", "--short", "HEAD"]);
  const commitFull = await shellSafe("git", ["rev-parse", "HEAD"]);
  const commitDate = await shellSafe("git", [
    "show",
    "-s",
    "--format=%cI",
    "HEAD",
  ]);
  const branch = await shellSafe("git", ["rev-parse", "--abbrev-ref", "HEAD"]);
  const dirty = await shellSafe("git", ["status", "--porcelain"]);

  const buildInfo = {
    commitShort: typeof commitShort === "string" ? commitShort.trim() : null,
    commitFull: typeof commitFull === "string" ? commitFull.trim() : null,
    commitDate: typeof commitDate === "string" ? commitDate.trim() : null,
    branch: typeof branch === "string" ? branch.trim() : null,
    dirty: typeof dirty === "string" ? dirty.trim().length > 0 : false,
    builtAt: new Date().toISOString(),
    repoUrl: "https://github.com/kitsunium/sdk",
  };
  await writeFile(BUILD_INFO_JSON, JSON.stringify(buildInfo, null, 2) + "\n");
  console.log(
    `[sync-versions] wrote ${BUILD_INFO_JSON} (commit=${buildInfo.commitShort ?? "?"}${buildInfo.dirty ? "+dirty" : ""})`,
  );
}

main().catch((err) => {
  console.error("[sync-versions] FAILED:", err);
  process.exit(1);
});
