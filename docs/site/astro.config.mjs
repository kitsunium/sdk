// Astro config — static site for the kitsunium/sdk documentation.
// `npm run prebuild` writes src/data/versions.json + build-info.json
// before this file is evaluated; the latter feeds the canonical
// `site` URL Astro emits for sitemap.xml / rss.xml entries.
//
// Redirects (/ → /<m>/<r>/ and /<m> → /<m>/<r>/) are real Astro
// pages under src/pages/index.astro + src/pages/[major]/index.astro,
// NOT Astro's `redirects:` map — the latter emits HTML5-light pages
// without an <html lang> wrapper, which makes Pagefind warn on every
// build. Real pages give us a proper <html lang="en"> shell plus a
// data-pagefind-ignore body so the search index stays clean.
import { defineConfig } from "astro/config";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import sitemap from "@astrojs/sitemap";
import expressiveCode from "astro-expressive-code";
import rehypeSlug from "rehype-slug";
import remarkGithubBlockquoteAlert from "remark-github-blockquote-alert";

//: gomarkdoc emits indented blocks from Go doc comments as ``` fences
//: with NO language tag (the doc-comment grammar has no language
//: affordance). Two distinct shapes land in those bare fences:
//:   1. real Go code samples (Quick start, Activation, Extension
//:      interfaces, Errors)
//:   2. markdown pipe-tables that the doc author indented so they
//:      survived through the Go-doc parser (e.g. the "What's shipped"
//:      Format-x-MIME table in pkg/v1/codec/codec.go)
//: Default both to Go and the table renders as red-keyword Go code —
//: ridiculous. Discriminate:
//:   - if every non-empty line starts with "|" → parse the bare fence
//:     into a real mdast `table` node so Astro emits proper <table>
//:     HTML (GFM is on by default)
//:   - otherwise → tag the code node `go` so astro-expressive-code
//:     applies the same Shiki github-dark + icon copy button as the
//:     ```go fences in USES.md
//: Plaintext snippets that match neither shape stay untagged.
function tryParsePipeTable(value) {
  const lines = value
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  if (lines.length < 2) return null;
  if (!lines.every((l) => l.startsWith("|"))) return null;
  //: second line MUST be the alignment row (--- / :--- / :-: / ---:).
  if (!/^\|[\s\-:|]+\|?$/.test(lines[1])) return null;
  const splitRow = (line) =>
    line
      .replace(/^\|/, "")
      .replace(/\|$/, "")
      .split("|")
      .map((c) => c.trim());
  const align = splitRow(lines[1]).map((c) => {
    const l = c.startsWith(":");
    const r = c.endsWith(":");
    if (l && r) return "center";
    if (r) return "right";
    if (l) return "left";
    return null;
  });
  const headerCells = splitRow(lines[0]);
  const bodyRows = lines.slice(2).map(splitRow);
  const cell = (text) => ({
    type: "tableCell",
    children: [{ type: "text", value: text }],
  });
  return {
    type: "table",
    align,
    children: [
      { type: "tableRow", children: headerCells.map(cell) },
      ...bodyRows.map((row) => ({
        type: "tableRow",
        children: row.map(cell),
      })),
    ],
  };
}

function remarkDefaultLangGo() {
  return (tree) => {
    const walk = (node) => {
      if (!node.children) return;
      for (let i = 0; i < node.children.length; i++) {
        const child = node.children[i];
        if (child.type === "code" && !child.lang) {
          const table = tryParsePipeTable(child.value || "");
          if (table) {
            node.children[i] = table;
            continue;
          }
          child.lang = "go";
          continue;
        }
        walk(child);
      }
    };
    walk(tree);
  };
}

const buildInfoPath = fileURLToPath(
  new URL("./src/data/build-info.json", import.meta.url),
);
const buildInfo = existsSync(buildInfoPath)
  ? JSON.parse(readFileSync(buildInfoPath, "utf8"))
  : {};

//: Derive the canonical site URL from build-info.repoUrl (set by
//: sync-versions). Pattern: github.com/<org>/<repo> →
//: https://<org>.github.io/<repo>. Override with DOCS_SITE_URL
//: in CI when deploying somewhere else (custom domain, GitLab pages).
function deriveSiteUrl(repoUrl) {
  if (process.env.DOCS_SITE_URL) return process.env.DOCS_SITE_URL;
  if (!repoUrl) return "https://example.invalid";
  const gh = repoUrl.match(/github\.com\/([^/]+)\/([^/]+?)(?:\.git)?\/?$/);
  //: ORIGIN only — the repo path is carried by `base` (a GitHub project page
  //: serves under /<repo>/). site + base then compose correctly for routes,
  //: assets, canonical, and sitemap.
  if (gh) return `https://${gh[1]}.github.io`;
  return repoUrl;
}
//: Base path. A GitHub project page serves at https://<org>.github.io/<repo>/,
//: so every absolute asset/link must be prefixed with /<repo> or it 404s
//: (ADR 0009 §accessible). A custom domain (DOCS_SITE_URL) or non-GitHub
//: remote serves at root. Override with DOCS_BASE if needed.
function deriveBase(repoUrl) {
  if (process.env.DOCS_BASE) return process.env.DOCS_BASE;
  if (process.env.DOCS_SITE_URL) return "/";
  const gh = (repoUrl || "").match(
    /github\.com\/[^/]+\/([^/]+?)(?:\.git)?\/?$/,
  );
  return gh ? `/${gh[1]}` : "/";
}
const site = deriveSiteUrl(buildInfo.repoUrl);
const base = deriveBase(buildInfo.repoUrl);

export default defineConfig({
  site,
  base,
  output: "static",
  build: {
    //: directory format so the catch-all generates real index.html files
    //: per route (avoids the redirect-loop trap with file format).
    format: "directory",
  },
  vite: {
    build: {
      //: Mermaid ships an entire diagramming engine (cytoscape, dagre,
      //: per-diagram-type renderers). Each chunk is lazily fetched
      //: only when a ```mermaid block is on the page, so the 600-700 KB
      //: "core + wardley" worst case never hits a reader who never
      //: views a diagram. Raise the warning limit so honest-sized
      //: chunks stop polluting the build log.
      chunkSizeWarningLimit: 1500,
    },
  },
  integrations: [
    //: Replaces the bare Shiki renderer with copy-button, language
    //: badge, optional line numbers, and a frame around blocks. Kept
    //: dark to align with our palette; switches with [data-theme]
    //: on <html> when the toggle flips.
    expressiveCode({
      themes: ["github-dark", "github-light"],
      themeCssSelector: (theme) =>
        theme.name === "github-light"
          ? "[data-theme='light']"
          : "[data-theme='dark']",
      styleOverrides: {
        borderRadius: "0.4rem",
        codeFontFamily:
          "ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace",
      },
    }),
    //: Emits sitemap.xml + a sitemap-index for SEO discoverability.
    //: Pages with `noindex` frontmatter can be excluded later via
    //: the `filter` option — none today.
    sitemap(),
  ],
  markdown: {
    //: Auto-id every heading (rehype-slug) so the right-side TOC,
    //: symbol search, and fragment URLs keep resolving. We deliberately
    //: skip rehype-autolink-headings — a visible "#" affordance next
    //: to every H2/H3 reads as noise even when opacity-hidden until
    //: hover. Deep-links still work via the heading id; users copy a
    //: section URL from the address bar or the sidebar.
    rehypePlugins: [rehypeSlug],
    //: GitHub-flavoured > [!NOTE] / [!TIP] / [!WARNING] / [!CAUTION]
    //: / [!IMPORTANT] blockquote alerts. CSS handles the styling
    //: by selector .markdown-alert(-note|-tip|-warning|-caution|-important).
    remarkPlugins: [remarkGithubBlockquoteAlert, remarkDefaultLangGo],
  },
});
