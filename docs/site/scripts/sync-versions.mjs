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
  rm,
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

// ─── Changelog generation ─────────────────────────────────────────
//
// Source of truth = git log. No CHANGELOG.md hand-edited file (the
// repo's commits are strictly conventional — see /workspace/CLAUDE.md).
// For tagged releases: window = previous-tag..current-tag.
// For "local": window = last 30 commits (no tags yet on this repo).

const COMMIT_DELIM = "---COMMIT-END---";
const COMMIT_FORMAT = `%H|%h|%cI|%s|%b${COMMIT_DELIM}`;

const CONVENTIONAL_RE =
  /^(feat|fix|perf|refactor|chore|docs|test|build|ci|style)(?:\(([^)]+)\))?(!)?:\s*(.+)$/;

const GROUP_LABEL = {
  feat: "Features",
  fix: "Fixes",
  perf: "Performance",
  refactor: "Refactors",
  docs: "Documentation",
  build: "Build",
  ci: "CI",
  test: "Tests",
  chore: "Chores",
  style: "Style",
  misc: "Other",
};

const GROUP_ORDER_FOR_CHANGELOG = [
  "feat",
  "fix",
  "perf",
  "refactor",
  "docs",
  "build",
  "ci",
  "test",
  "style",
  "chore",
  "misc",
];

function classifyCommit(subject) {
  const m = subject.match(CONVENTIONAL_RE);
  if (!m)
    return { type: "misc", scope: null, breaking: false, summary: subject };
  return {
    type: m[1],
    scope: m[2] ?? null,
    breaking: m[3] === "!",
    summary: m[4],
  };
}

async function gitLogCommits(sourceRoot, refSpec) {
  const args = ["log", `--pretty=format:${COMMIT_FORMAT}`];
  for (const part of refSpec) args.push(part);
  const out = await shellSafe("git", args, { cwd: sourceRoot });
  if (typeof out !== "string") return [];
  return out
    .split(COMMIT_DELIM)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      //: Subject can contain "|" (e.g. piped-shell commands quoted in a
      //: commit summary). Split only on the first 4 separators to keep
      //: the body intact even if it carries pipes.
      const idx = [];
      let pos = -1;
      for (let i = 0; i < 4; i++) {
        pos = s.indexOf("|", pos + 1);
        if (pos === -1) break;
        idx.push(pos);
      }
      if (idx.length < 4) return null;
      const sha = s.slice(0, idx[0]);
      const short = s.slice(idx[0] + 1, idx[1]);
      const date = s.slice(idx[1] + 1, idx[2]);
      const subject = s.slice(idx[2] + 1, idx[3]);
      const body = s.slice(idx[3] + 1);
      return { sha, short, date, subject, body };
    })
    .filter(Boolean);
}

function renderChangelogMarkdown(commits, release, major, repoUrl) {
  const grouped = new Map();
  for (const c of commits) {
    const meta = classifyCommit(c.subject);
    if (meta.type === "chore" && !meta.breaking) continue; //: drop chore noise from public changelog
    if (!grouped.has(meta.type)) grouped.set(meta.type, []);
    grouped.get(meta.type).push({ ...c, ...meta });
  }
  const lines = [];
  for (const t of GROUP_ORDER_FOR_CHANGELOG) {
    const rows = grouped.get(t);
    if (!rows || rows.length === 0) continue;
    lines.push(`### ${GROUP_LABEL[t]}`);
    lines.push("");
    for (const r of rows) {
      const scope = r.scope ? `**${r.scope}**: ` : "";
      const breaking = r.breaking ? " · ⚠️ breaking" : "";
      const commitLink = repoUrl
        ? `[\`${r.short}\`](${repoUrl}/commit/${r.sha})`
        : `\`${r.short}\``;
      lines.push(`- ${commitLink} ${scope}${r.summary}${breaking}`);
    }
    lines.push("");
  }
  return lines.join("\n");
}

async function materialiseChangelog(major, release, sourceRoot, dest, repoUrl) {
  //: Window = last 30 commits for "local" (no tags yet on this repo);
  //: for a real tag we'd pass `<prev-tag>..<this-tag>`. The tooling
  //: is ready — the prev-tag wiring lands the day the first
  //: pkg/<major>/vX.Y.Z tag exists.
  const refSpec =
    release === LOCAL_RELEASE ? ["-n", "30", "HEAD"] : ["-n", "100", release];
  const commits = await gitLogCommits(sourceRoot, refSpec);
  const body = renderChangelogMarkdown(commits, release, major, repoUrl);
  const title = RESERVED.changelog?.label ?? "Changelog";
  await writeFile(
    join(dest, "changelog.md"),
    frontmatter({
      title,
      description: `What changed in ${release}`,
      source: `git log`,
    }) +
      `# ${title}\n\n` +
      `_Generated from git log (${commits.length} commit${commits.length === 1 ? "" : "s"} ` +
      `${release === LOCAL_RELEASE ? "in the last 30 on this branch" : "in this release"}). ` +
      `Conventional-commit prefixes drive the grouping; bare \`chore:\` ` +
      `entries are omitted to keep the log signal-rich._\n\n` +
      body,
  );

  //: WhatsNew banner data — top 3 feat + top 2 fix, most recent first.
  //: Banner shown ABOVE the article on the Home page only. JSON lives in
  //: src/data/ rather than content/docs/ because Astro content collections
  //: don't index JSON — we import it dynamically in WhatsNew.astro.
  const tops = [];
  let feat = 0;
  let fix = 0;
  for (const c of commits) {
    const meta = classifyCommit(c.subject);
    if (meta.type === "feat" && feat < 3) {
      tops.push({ ...meta, sha: c.sha, short: c.short, date: c.date });
      feat++;
    } else if (meta.type === "fix" && fix < 2) {
      tops.push({ ...meta, sha: c.sha, short: c.short, date: c.date });
      fix++;
    }
    if (feat >= 3 && fix >= 2) break;
  }
  const wnPayload = {
    release,
    major,
    generatedAt: new Date().toISOString(),
    repoUrl: repoUrl ?? null,
    entries: tops,
  };
  await mkdir(join(SITE_ROOT, "src", "data"), { recursive: true });
  await writeFile(
    join(SITE_ROOT, "src", "data", `whats-new-${release}-${major}.json`),
    JSON.stringify(wnPayload, null, 2) + "\n",
  );
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
  //: Wipe the destination before rewriting so stale entries from a
  //: previous layout (e.g. <major>/<release> pre-flip) can't bleed
  //: through. Astro 5's glob-loader emits "Duplicate id" warnings
  //: when a file appears in cache AND on disk after a layout change;
  //: this guarantees the on-disk set is exactly what the current run
  //: produces, nothing more.
  await rm(dest, { recursive: true, force: true });
  await mkdir(dest, { recursive: true });

  const pkgMajor = join(sourceRoot, "pkg", major);
  const claudeMd = join(sourceRoot, "CLAUDE.md");
  const adrDir = join(sourceRoot, "docs", "adr");

  // 1. Landing page (index.md): the repo README is now the
  // authoritative "what is this SDK" doc — same vocabulary on
  // GitHub and on the docs portal Home (cf.
  // .claude/contexts/home-and-changelog.md). pkg/<major>/CLAUDE.md
  // stays as a maintainer-only file (not rendered on the site).
  // We prepend frontmatter so the page-header h1 reads "Home"
  // (RESERVED label) instead of the README's own "# kitsunium/sdk".
  const landingTitle = RESERVED.index.label ?? major;
  const readme = join(sourceRoot, "README.md");
  if (existsSync(readme)) {
    const body = await readFile(readme, "utf8");
    //: The README's package links (`./pkg/v1/codec`) resolve to the package
    //: directory on GitHub, but the docs portal renders each sub-package as a
    //: flat sibling page (`/<major>/codec`, written as `${sub.name}.md` below).
    //: Rewrite `](./pkg/<major>/<name>)` → `](./<name>/)` so the Home table
    //: links to the portal routes instead of 404ing. Trailing slash matches
    //: the ADR-index convention. Deeper links (BENCH.md, …) are left alone.
    const portalBody = body
      .replace(
        new RegExp("\\]\\(\\./pkg/" + major + "/([^)/]+)/?\\)", "g"),
        "](./$1/)",
      )
      //: README's ./LICENSE resolves to the repo root on GitHub, but the
      //: portal has no LICENSE page — point the portal copy at the GitHub
      //: blob (same convention as the BENCH.md link on /contributors/).
      .replace(
        /\]\(\.\/LICENSE\)/g,
        "](https://github.com/kitsunium/sdk/blob/HEAD/LICENSE)",
      );
    await writeFile(
      join(dest, "index.md"),
      frontmatter({
        title: landingTitle,
        description: `Project overview`,
        source: `README.md`,
      }) + portalBody,
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
      if (!existsSync(readme)) continue;
      const raw = await readFile(readme, "utf8");
      const indexStripped = stripGomarkdocIndex(raw);

      //: Split the gomarkdoc output into:
      //:   narrative — the Go doc comment content (pitch + Quick start
      //:               + Activation + ...). Reader-curated prose.
      //:   symbols   — the auto-appended Constants / Variables /
      //:               func X / type X / "Generated by gomarkdoc"
      //:               dump. Useful as a reference but a wall of
      //:               text when read top-to-bottom.
      //: Boundary = first H2 that matches the gomarkdoc auto-section
      //: shape. Wrapping `symbols` in <details> turns it into an
      //: opt-in reference panel instead of a flood.
      const symbolBoundary = indexStripped.match(
        /^(## (?:Constants|Variables|func\s|type\s)|## Generated by gomarkdoc)/m,
      );
      let narrative = indexStripped;
      let symbols = "";
      if (symbolBoundary) {
        narrative =
          indexStripped.slice(0, symbolBoundary.index).trimEnd() + "\n";
        symbols = indexStripped.slice(symbolBoundary.index);
      }

      //: Inject USES.md ("Use cases" — tabs per Format / sink / pattern)
      //: between the narrative and the API reference dump. Replicable
      //: across packages: drop a USES.md alongside README.md and it's
      //: picked up automatically.
      const usesPath = join(pkgMajor, sub.name, "USES.md");
      let body = narrative;
      if (existsSync(usesPath)) {
        body += "\n" + (await readFile(usesPath, "utf8")) + "\n";
      }
      if (symbols.length > 0) {
        body +=
          `\n<details class="api-reference">\n` +
          `<summary><strong>API reference</strong> — full constants, variables, functions, types</summary>\n\n` +
          symbols +
          `\n</details>\n`;
      }

      //: Append the package's BENCH.md (if any) under a "## Benchmarks"
      //: H2 — same pattern across codec / errs / logger.
      const benchPath = join(pkgMajor, sub.name, "BENCH.md");
      if (existsSync(benchPath)) {
        const benchRaw = await readFile(benchPath, "utf8");
        const benchBody = benchRaw
          .replace(/^<!--[^>]*-->\s*/, "")
          .replace(/^#\s+.+?\n+/m, "");
        body += `\n\n## Benchmarks\n\n${benchBody}`;
      }

      await writeFile(
        join(dest, `${sub.name}.md`),
        frontmatter({ source: `pkg/${major}/${sub.name}/README.md` }) + body,
      );
    }
    //: Standalone /benchmarks/ page intentionally NOT materialised
    //: anymore (cf. .claude/contexts/docs-sidebar-rethink.md — 0/7
    //: SDKs surface a benchmarks page in main nav). The 37 KB
    //: pkg/v1/codec/BENCH.md stays accessible via GitHub and will
    //: be re-surfaced inline at the bottom of /codec/ in P3
    //: (.claude/contexts/package-inline-benchmarks.md).
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
      //: CLAUDE.md is a maintainer-only file (per docs/adr/CLAUDE.md), not an
      //: ADR — skip it so it neither gets a public /adr/CLAUDE/ page nor an
      //: index row.
      if (/^claude\.md$/i.test(f)) continue;
      const raw = await readFile(join(adrDir, f), "utf8");
      //: Neutralise links to internal `.claude/` files (plan/context
      //: provenance in some ADR References sections): `.claude/` is not
      //: published to the portal, so the link would 404. Drop the href,
      //: keep the label text. The source ADR is immutable — only the
      //: rendered copy is rewritten.
      const portalRaw = raw.replace(
        /\[([^\]]+)\]\([^)]*\.claude\/[^)]*\)/g,
        "$1",
      );
      await writeFile(
        join(adrDest, f),
        frontmatter({ source: `docs/adr/${f}` }) + portalRaw,
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
  //
  // What we materialise vs what we drop (cf. docs-sidebar-rethink):
  //   keep:    concepts (← was 'architecture', sliced from root CLAUDE.md)
  //   drop:    philosophy (0/7 SDKs surface this; lives in CLAUDE.md only)
  //   move:    verification → folded into contributors.md (hidden page,
  //            reachable via Sidebar footer 'For contributors ↗')
  //
  // "Getting started" is no longer synthesised from CLAUDE.md's
  // 'How to work' section — that text is the MAINTAINER workflow
  // (/plan, /do, /git --commit, make lint, bazel test) which has no
  // place on a consumer-onboarding page. Source is now the dedicated
  // /workspace/docs/getting-started.md file.
  const sections = await extractClaudeMdSections(claudeMd);
  const pageMap = [["concepts", "Architecture at a glance"]];
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

  // Getting started — sourced from /workspace/docs/getting-started.md
  // (a real consumer-onboarding document, not a slice of maintainer
  // workflow). Prepend frontmatter so the page-header h1 reads
  // "Getting started" (RESERVED label) consistently with the sidebar.
  const gsPath = join(sourceRoot, "docs", "getting-started.md");
  if (existsSync(gsPath)) {
    const gsTitle = RESERVED["getting-started"]?.label ?? "Getting started";
    const gsBody = await readFile(gsPath, "utf8");
    //: drop the leading H1 from the source — the page-header re-renders
    //: the title from frontmatter, and the .md-body--strip-first-h1
    //: rule hides any leading h1 to avoid duplication.
    const stripped = gsBody.replace(/^#\s+.+?\n+/m, "");
    await writeFile(
      join(dest, "getting-started.md"),
      frontmatter({
        title: gsTitle,
        description: "Five-minute install + first program",
        source: "docs/getting-started.md",
      }) + stripped,
    );
  }

  // 5. Contributors page (hidden from main sidebar nav; surfaced via
  // Sidebar footer "For contributors ↗"). Bundles together the
  // maintainer-facing material that doesn't belong in a consumer
  // portal: ADR pointer, verification gates, where the bench data
  // lives on GitHub.
  const contribTitle = RESERVED.contributors.label ?? "For contributors";
  const verificationBody = sections["Verification"] ?? "";
  const contributorsBody =
    `# ${contribTitle}\n\n` +
    `Maintainer-facing material — kept reachable but out of the main ` +
    `consumer navigation. If you're consuming the SDK as a library, you ` +
    `won't need any of this.\n\n` +
    `## Architecture decisions\n\n` +
    `Every cross-cutting decision is recorded as an ADR. The set is ` +
    `frozen per release (snapshotted at tag time). Browse them at ` +
    `[ADRs](../adr/).\n\n` +
    `## Performance\n\n` +
    `Codec benchmarks (18 formats × Marshal/Unmarshal × small/med/large) ` +
    `are generated by \`pkg/${major}/codec/codec_bench_test.go\`. The full ` +
    `report lives at [\`pkg/${major}/codec/BENCH.md\`](https://github.com/kitsunium/sdk/blob/HEAD/pkg/${major}/codec/BENCH.md). ` +
    `Each package surfaces its key bench numbers at the bottom of its ` +
    `own page (codec, errs, logger).\n\n` +
    (verificationBody ? `## Verification gates\n\n${verificationBody}\n` : "");
  await writeFile(
    join(dest, "contributors.md"),
    frontmatter({
      title: contribTitle,
      description: "Maintainer onboarding — ADRs, benchmarks, verification",
      source: `CLAUDE.md`,
    }) + contributorsBody,
  );

  // 6. Changelog (auto-generated from git log between tags).
  // For "local" we window over the last 30 commits — until the first
  // pkg/<major>/vX.Y.Z tag lands, that's the most honest signal.
  // Also emits src/data/whats-new-<release>-<major>.json consumed by
  // the <WhatsNew /> banner injected at the top of the Home page.
  const repoUrl = "https://github.com/kitsunium/sdk";
  await materialiseChangelog(major, release, sourceRoot, dest, repoUrl);
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

  // 0. Single deterministic wipe of the materialised content tree +
  // Astro's content-collection cache. Two reasons:
  //   (a) Layout migrations (e.g. <major>/<release> → <release>/<major>)
  //       leave orphan directories the per-release wipes inside
  //       materialiseRelease can't reach.
  //   (b) Astro 5's glob loader can fire a "Duplicate id" warning on
  //       its initial scan when a file appears in cache AND on disk
  //       after a layout change. Nuking .astro/ alongside the on-disk
  //       wipe guarantees astro starts from a known-empty store.
  // The wipes are millisecond-scoped on a tree of ~20 markdown files.
  // .astro at the site root holds the schema + types, but the actual
  // content-collection STORE (the source of phantom duplicate ids)
  // lives in node_modules/.astro/data-store.json — wipe both.
  await rm(CONTENT_ROOT, { recursive: true, force: true });
  await rm(join(SITE_ROOT, ".astro"), { recursive: true, force: true });
  await rm(join(SITE_ROOT, "node_modules", ".astro"), {
    recursive: true,
    force: true,
  });
  await mkdir(CONTENT_ROOT, { recursive: true });

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
