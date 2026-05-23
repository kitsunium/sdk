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
  if (gh) return `https://${gh[1]}.github.io/${gh[2]}`;
  return repoUrl;
}
const site = deriveSiteUrl(buildInfo.repoUrl);

export default defineConfig({
  site,
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
    remarkPlugins: [remarkGithubBlockquoteAlert],
  },
});
