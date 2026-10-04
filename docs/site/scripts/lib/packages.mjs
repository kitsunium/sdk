// docs/site/scripts/lib/packages.mjs — which directories of pkg/<major>/
// become package pages, and how the root README's links reach them.
//
// A page is materialised for EVERY directory under pkg/<major>/ that holds a
// README.md, at any depth, at the path it has under pkg/<major>/. Since
// ADR 0155 the public packages sit in family directories — pkg/v1/data/codec,
// pkg/v1/observe/logger — so a portal that listed only pkg/<major>'s
// immediate children would publish the four packages left at the root (errs,
// clock, crypto, proc) and silently drop every other one. The portal mirrors
// the import path instead: /<release>/<major>/data/codec/. A family directory
// that is not itself a package (pkg/v1/data) holds no README.md and gets no
// page; one that is (pkg/v1/crypto, the AEAD facade) gets its own page beside
// its members'. A release cut before the families existed is walked the same
// way, so its snapshot keeps its flat routes.

import { readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { join, posix } from "node:path";

/**
 * Directory names the walk never enters, whatever their depth: `internal`
 * is not importable by a consumer, `testdata` is the go tool's own fixture
 * directory, and a name starting with "." or "_" is one the go tool ignores.
 */
const SKIPPED = new Set(["internal", "testdata"]);

/**
 * @param {string} name
 * @returns {boolean}
 */
function skipped(name) {
  return SKIPPED.has(name) || name.startsWith(".") || name.startsWith("_");
}

/**
 * Every directory beneath `root` holding a README.md, as a slash-separated
 * path relative to `root`, sorted. `root` itself is never listed, and a
 * missing `root` lists nothing.
 *
 * @param {string} root — pkg/<major>/ of the tree being materialised
 * @returns {Promise<string[]>}
 */
export async function listPackageDirs(root) {
  /** @type {string[]} */
  const found = [];
  /**
   * @param {string} dir
   * @param {string} rel
   */
  async function walk(dir, rel) {
    const entries = await readdir(dir, { withFileTypes: true });
    for (const entry of entries) {
      if (!entry.isDirectory() || skipped(entry.name)) continue;
      const childRel = rel === "" ? entry.name : `${rel}/${entry.name}`;
      const childDir = join(dir, entry.name);
      if (existsSync(join(childDir, "README.md"))) found.push(childRel);
      await walk(childDir, childRel);
    }
  }
  if (existsSync(root)) await walk(root, "");
  return found.sort();
}

/**
 * Points the root README's relative links somewhere they resolve once the
 * README is rendered as the Home page of /<release>/<major>/. A link to a
 * package directory — `](./pkg/<major>/<path>)`, with or without a trailing
 * slash, <path> one of `packages` — becomes `](./<path>/)`, the package's
 * page. Every other relative link names a file or directory the portal does
 * not publish — LICENSE, a BENCH.md, a module of the framework
 * (`./framework/git` — ADR 0158) — and is pointed at it on GitHub under
 * `blobBase` instead of 404ing. Absolute links and in-page anchors are left
 * as written.
 *
 * @param {string} body — the README's markdown
 * @param {string} major — "v1"
 * @param {Iterable<string>} packages — listPackageDirs' answer
 * @param {string} blobBase — "https://github.com/<owner>/<repo>/blob/HEAD"
 * @returns {string}
 */
export function rewriteReadmeLinks(body, major, packages, blobBase) {
  const pages = new Set(packages);
  const prefix = `pkg/${major}/`;
  return body.replace(/\]\(\.\/([^)\s]+)\)/g, (_whole, target) => {
    if (target.startsWith(prefix)) {
      const rel = target.slice(prefix.length).replace(/\/$/, "");
      if (pages.has(rel)) return `](./${rel}/)`;
    }
    return `](${blobBase}/${target})`;
  });
}

/**
 * Points the relative links of a package's BENCH.md or USES.md where they
 * resolve once the file is part of the package's page: a link to another
 * package's BENCH.md — `crypto/agree/BENCH.md` names the family's as
 * `../BENCH.md` — becomes a link to that page's Benchmarks section, one to a
 * package's README.md or directory a link to its page, and any other
 * relative file of the repository a link to it under `blobBase`. Absolute
 * links, in-page anchors and links leaving the repository are left as
 * written; so is every line of a fenced code block.
 *
 * @param {string} body — the file's markdown
 * @param {string} major — "v1"
 * @param {string} from — the package's path under pkg/<major>/, "crypto/agree"
 * @param {Iterable<string>} packages — listPackageDirs' answer
 * @param {string} blobBase — "https://github.com/<owner>/<repo>/blob/<ref>"
 * @returns {string}
 */
export function rewritePackageDocLinks(body, major, from, packages, blobBase) {
  const pages = new Set(packages);
  const prefix = `pkg/${major}/`;
  const pageLink = (rel, fragment) => {
    const up = posix.relative(from, rel);
    return up === "" ? fragment || "./" : `${up}/${fragment}`;
  };
  const rewrite = (line) =>
    line.replace(/\]\(([^)\s<>]+)\)/g, (whole, target) => {
      if (/^(?:[a-z][a-z0-9+.-]*:|\/|#)/i.test(target)) return whole;
      if (!/^(?:\.{1,2}\/)*[\w.-]+(?:\/[\w.-]+)*\/?(?:#[\w.-]*)?$/.test(target))
        return whole;
      const hash = target.indexOf("#");
      const path = hash < 0 ? target : target.slice(0, hash);
      const fragment = hash < 0 ? "" : target.slice(hash);
      const resolved = posix.normalize(posix.join(prefix + from, path));
      if (resolved.startsWith("../")) return whole;
      if (resolved.startsWith(prefix)) {
        const inPkg = resolved.slice(prefix.length).replace(/\/$/, "");
        const bench = inPkg.match(/^(.+)\/BENCH\.md$/);
        if (bench && pages.has(bench[1]))
          return `](${pageLink(bench[1], fragment || "#benchmarks")})`;
        const readme = inPkg.replace(/\/README\.md$/, "");
        if (pages.has(readme)) return `](${pageLink(readme, fragment)})`;
      }
      return `](${blobBase}/${resolved}${fragment})`;
    });
  let fenced = false;
  return body
    .split("\n")
    .map((line) => {
      if (/^\s*(```|~~~)/.test(line)) {
        fenced = !fenced;
        return line;
      }
      return fenced ? line : rewrite(line);
    })
    .join("\n");
}
