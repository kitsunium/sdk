// docs/site/scripts/lib/adr.mjs — how an ADR's relative links reach their
// target once the ADR is a portal page.
//
// An ADR is written for the repository: `[ADR 0162](0162-the-sdk-is-one-module.md)`
// names the file beside it, and GitHub renders that. The portal serves each
// ADR as a page at /<release>/<major>/adr/<slug>/, so the same link resolves
// to /adr/<this-slug>/0162-….md, which does not exist — 768 links of the
// working tree's ADRs did, before this. A link to another ADR becomes a link
// to its page; a link to any other Markdown file of the repository becomes a
// link to it on GitHub. Fenced code is left as written.

import { posix } from "node:path";

const LINK = /\]\(([^)\s]+)\)/g;
const MARKDOWN_FILE = /^(?:\.{1,2}\/)*[\w.-]+(?:\/[\w.-]+)*\.md(#[\w.-]*)?$/;

/**
 * Rewrites the relative Markdown links of an ADR read from docs/adr/: a link
 * to an ADR of `adrs` — `0001-foo.md`, `./0001-foo.md`, `../adr/0001-foo.md`,
 * a fragment kept — points at its portal page, `../0001-foo/`; a link to any
 * other `.md` file of the repository points at it under `blobBase`. Absolute
 * links, site paths, in-page anchors, links outside the repository and every
 * line of a fenced code block are left as written.
 *
 * @param {string} body — the ADR's markdown
 * @param {Iterable<string>} adrs — the ADR files' names without ".md"
 * @param {string} blobBase — "https://github.com/<owner>/<repo>/blob/<ref>"
 * @returns {string}
 */
export function rewriteAdrLinks(body, adrs, blobBase) {
  const known = new Set(adrs);
  const rewrite = (line) =>
    line.replace(LINK, (whole, target) => {
      if (!MARKDOWN_FILE.test(target)) return whole;
      const hash = target.indexOf("#");
      const path = hash < 0 ? target : target.slice(0, hash);
      const fragment = hash < 0 ? "" : target.slice(hash);
      const resolved = posix.normalize(posix.join("docs/adr", path));
      if (resolved.startsWith("../")) return whole;
      const adr = resolved.match(/^docs\/adr\/([^/]+)\.md$/);
      if (adr && known.has(adr[1])) return `](../${adr[1]}/${fragment})`;
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
