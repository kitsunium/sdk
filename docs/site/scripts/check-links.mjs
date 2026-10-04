#!/usr/bin/env node
// docs/site/scripts/check-links.mjs — the built site's link check.
//
// Crawls dist/ after `npm run build` and resolves every link a page writes —
// each href and src of every HTML page — and every url of every ⌘K symbol
// index (dist/_search/symbols-*.json), the way a browser would on the deployed
// project page:
//
//   - an absolute path must sit under the deploy base (/<repo>/), or it 404s
//     there — the missing-base bug docs/site/CLAUDE.md names;
//   - the page or file it names must exist in dist/ (a directory is its
//     index.html);
//   - a fragment must name an element of the target page, by id or by an
//     <a name>: a ⌘K entry or a doc link landing on the top of a page has
//     lost the record it promised.
//
// One exception, counted and printed but not failed: a fragment on a page of
// a release whose tree has no docs/api (versions.json flags the others
// `api`). Such a page is that release's README as gomarkdoc wrote it at the
// tag — `[New](#New)` with no anchor of that name — and a published tag
// cannot be edited. A release with docs/api has its anchors written by the
// portal (ApiSection.astro), and every fragment on it must resolve.
//
// External links (another origin, mailto:, data:) are not fetched: the check
// runs offline. The base is read from build-info.json the way astro.config.mjs
// derives it (DOCS_BASE, else DOCS_SITE_URL means "/", else /<repo>), or from
// --base.
//
//   node scripts/check-links.mjs [--dist dist] [--base /sdk] [--show 40]
//
// Exit 0 when every link resolves; 1 with each broken link named, grouped by
// what is wrong — the first --show of each group —, otherwise.

import { readdir, readFile } from "node:fs/promises";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve, posix } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SITE_ROOT = resolve(__dirname, "..");

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  return i > 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

/** The deploy base, "" or "/<repo>", as astro.config.mjs derives it. */
function deployBase() {
  const explicit = arg("--base", null);
  if (explicit !== null) return explicit.replace(/\/+$/, "");
  if (process.env.DOCS_BASE) return process.env.DOCS_BASE.replace(/\/+$/, "");
  if (process.env.DOCS_SITE_URL) return "";
  let repoUrl = "";
  try {
    repoUrl = JSON.parse(
      readFileSync(join(SITE_ROOT, "src", "data", "build-info.json"), "utf8"),
    ).repoUrl;
  } catch {
    /* no build info: root-served */
  }
  const gh = (repoUrl || "").match(
    /github\.com\/[^/]+\/([^/]+?)(?:\.git)?\/?$/,
  );
  return gh ? `/${gh[1]}` : "";
}

const DIST = resolve(SITE_ROOT, arg("--dist", "dist"));
const SHOW = Number(arg("--show", "40"));

/**
 * The releases whose tree had no docs/api, by version: versions.json lists
 * every release built and flags `api` on the others. None when versions.json
 * is missing — every fragment is then judged.
 */
function releasesWithoutApi() {
  try {
    const versions = JSON.parse(
      readFileSync(join(SITE_ROOT, "src", "data", "versions.json"), "utf8"),
    );
    return new Set(
      versions.flatMap((v) =>
        v.releases.filter((r) => !r.api).map((r) => r.version),
      ),
    );
  } catch {
    return new Set();
  }
}

/** decodeURIComponent, or the text as written when it is malformed. */
function decode(s) {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

/** Every file under dist/, relative with "/", sorted. */
async function listFiles(root) {
  const out = [];
  async function walk(dir, rel) {
    for (const e of await readdir(dir, { withFileTypes: true })) {
      const r = rel ? `${rel}/${e.name}` : e.name;
      if (e.isDirectory()) await walk(join(dir, e.name), r);
      else out.push(r);
    }
  }
  await walk(root, "");
  return out.sort();
}

const ATTR = /\s(?:href|src)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;
const ID = /\s(?:id|name)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;

/** HTML attribute values decoded the way a browser reads them. */
function decodeEntities(s) {
  return s.replace(/&(#x[0-9a-f]+|#\d+|amp|lt|gt|quot|#39|apos);/gi, (m, e) => {
    const k = e.toLowerCase();
    if (k === "amp") return "&";
    if (k === "lt") return "<";
    if (k === "gt") return ">";
    if (k === "quot") return '"';
    if (k === "#39" || k === "apos") return "'";
    if (k.startsWith("#x"))
      return String.fromCodePoint(parseInt(k.slice(2), 16));
    return String.fromCodePoint(parseInt(k.slice(1), 10));
  });
}

/** Strips what a link scan must not read: scripts, styles, comments. */
function linkableHTML(html) {
  return html
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<script\b[\s\S]*?<\/script>/gi, "")
    .replace(/<style\b[\s\S]*?<\/style>/gi, "");
}

async function main() {
  if (!existsSync(DIST)) {
    console.error(`[check-links] ${DIST} missing — run npm run build first`);
    process.exit(1);
  }
  const base = deployBase();
  const legacy = releasesWithoutApi();
  const files = await listFiles(DIST);
  const fileSet = new Set(files);
  const pages = files.filter((f) => f.endsWith(".html"));

  /** @type {Map<string, Set<string>>} ids and names of a page, lazily */
  const anchorsOf = new Map();
  async function anchors(page) {
    let set = anchorsOf.get(page);
    if (!set) {
      set = new Set();
      const html = await readFile(join(DIST, page), "utf8");
      for (const m of html.matchAll(ID)) set.add(decodeEntities(m[1] ?? m[2]));
      anchorsOf.set(page, set);
    }
    return set;
  }

  /** The dist file a site path names, or null. */
  function target(path) {
    const p = path.replace(/^\/+/, "");
    if (p === "" || p.endsWith("/")) {
      const index = `${p}index.html`;
      return fileSet.has(index) ? index : null;
    }
    if (fileSet.has(p)) return p;
    if (fileSet.has(`${p}/index.html`)) return `${p}/index.html`;
    return null;
  }

  /** @type {Map<string, string[]>} problems by kind */
  const problems = new Map();
  const report = (kind, line) => {
    if (!problems.has(kind)) problems.set(kind, []);
    problems.get(kind).push(line);
  };
  let checked = 0;
  /** fragments missing on a page of a release without docs/api */
  let frozen = 0;

  /**
   * Resolves one link written on `from` (a dist page, or a label for an
   * index): returns nothing, reports what is broken.
   */
  async function check(from, fromURL, raw) {
    const link = decodeEntities(raw.trim());
    if (link === "" || /^(?:[a-z][a-z0-9+.-]*:|\/\/)/i.test(link)) return;
    checked++;
    const hash = link.indexOf("#");
    const pathPart = hash < 0 ? link : link.slice(0, hash);
    const fragment = hash < 0 ? null : decode(link.slice(hash + 1));
    const noQuery = pathPart.replace(/\?.*$/, "");
    let sitePath;
    if (noQuery === "") {
      sitePath = fromURL;
    } else if (noQuery.startsWith("/")) {
      if (base && noQuery !== base && !noQuery.startsWith(`${base}/`)) {
        report("outside the deploy base", `${from}: ${link}`);
        return;
      }
      sitePath = decode(noQuery.slice(base.length) || "/");
    } else {
      sitePath = posix.normalize(
        posix.join(posix.dirname(`${fromURL}x`), decode(noQuery)),
      );
      if (noQuery.endsWith("/") && !sitePath.endsWith("/")) sitePath += "/";
    }
    const file = target(sitePath);
    if (!file) {
      report("no such page or file", `${from}: ${link}`);
      return;
    }
    if (
      fragment &&
      file.endsWith(".html") &&
      !(await anchors(file)).has(fragment)
    ) {
      if (legacy.has(file.split("/")[0]) && legacy.has(from.split("/")[0])) {
        frozen++;
        return;
      }
      report("no such anchor on the page", `${from}: ${link}`);
    }
  }

  for (const page of pages) {
    const html = linkableHTML(await readFile(join(DIST, page), "utf8"));
    //: the page's own URL path, relative to the base: a/b/index.html → /a/b/
    const url = `/${page.replace(/(^|\/)index\.html$/, "$1")}`;
    const seen = new Set();
    for (const m of html.matchAll(ATTR)) {
      const raw = m[1] ?? m[2];
      if (seen.has(raw)) continue;
      seen.add(raw);
      await check(page, url, raw);
    }
  }

  //: every ⌘K entry is a link too: the client prefixes the base to its url.
  let entries = 0;
  for (const f of files.filter((x) => /^_search\/symbols-.*\.json$/.test(x))) {
    const index = JSON.parse(await readFile(join(DIST, f), "utf8"));
    for (const s of index.symbols ?? []) {
      entries++;
      await check(f, "/", `${base}${s.url}`);
    }
  }

  const broken = [...problems.values()].reduce((n, l) => n + l.length, 0);
  console.log(
    `[check-links] ${pages.length} pages, ${checked} links (${entries} ⌘K entries) under base "${base || "/"}": ${broken} broken`,
  );
  if (frozen > 0) {
    console.log(
      `[check-links] ${frozen} fragments missing on the README pages of releases without docs/api — frozen at their tag, not failed`,
    );
  }
  for (const [kind, lines] of problems) {
    console.log(`\n${kind} (${lines.length}):`);
    for (const l of lines.slice(0, SHOW)) console.log(`  ${l}`);
    if (lines.length > SHOW) console.log(`  … ${lines.length - SHOW} more`);
  }
  process.exit(broken === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error("[check-links] FAILED:", err);
  process.exit(1);
});
