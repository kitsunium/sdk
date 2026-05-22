// markdown.ts — shared helpers for loading + rendering markdown content
// at build time. Astro pages call readSourceMarkdown(relPath) to pull a
// file from the workspace, then renderMarkdown(text) to convert it to
// HTML via marked. Both calls run during the static build so the output
// HTML is self-contained.
import fs from "node:fs";
import path from "node:path";
import { marked } from "marked";

//: workspace root — Astro runs from docs/site/, so ../../ lands at the
//: repo root. process.cwd() is stable across dev/build/preview commands.
const WORKSPACE = path.resolve(process.cwd(), "../..");

/**
 * Read a UTF-8 file from the workspace, given a path relative to the
 * repository root (e.g. `pkg/v1/codec/README.md`). Throws when the file
 * is missing — the build should fail loudly so a renamed source file
 * doesn't ship a 404.
 */
export function readSourceMarkdown(relPath: string): string {
  const abs = path.join(WORKSPACE, relPath);
  return fs.readFileSync(abs, "utf-8");
}

/**
 * Parse a markdown blob through marked with GFM (tables + fenced code).
 * The result is HTML-safe relative to trusted sources — every input
 * file is checked into the repo, never user-supplied.
 */
export function renderMarkdown(md: string): string {
  marked.use({ gfm: true });
  return marked.parse(md) as string;
}

/**
 * Strip the first H1 heading from a markdown blob. Useful when a page
 * supplies its own title chrome and doesn't want the document's H1 to
 * duplicate it inside the body.
 */
export function stripTopHeading(md: string): string {
  //: dropped on the first H1 only; subsequent H1s (rare in our docs)
  //: stay intact so the table-of-contents heuristic still works.
  return md.replace(/^#\s+.+\n+/, "");
}

/**
 * Best-effort extraction of the front-matter "envelope" key/value lines
 * from BENCH.md. Used by the benchmarks page to show provenance in the
 * page header without re-parsing the whole markdown table.
 */
export function extractEnvelopeField(md: string, key: string): string {
  const re = new RegExp(`\\|\\s*${key}\\s*\\|\\s*([^|]+?)\\s*\\|`);
  const m = md.match(re);
  return m ? m[1].trim() : "(unknown)";
}
