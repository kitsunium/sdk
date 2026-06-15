// docs/site/src/content.config.ts — content collection schema (Astro 5).
//
// The `docs` collection holds the versioned markdown tree materialised
// by docs/site/scripts/sync-versions.mjs into
// src/content/docs/<release>/<major>/ — release is the time axis
// (local / v0.1.0), major the API surface (v1 / v2). We use the
// explicit glob loader (not the legacy `type: "content"`) so each
// .md file is processed exactly once — the legacy form double-walks
// the tree alongside the glob loader and emits "Duplicate id"
// warnings for every entry on Astro 5.
//
// Schema is intentionally loose: most files we copy in are READMEs
// without frontmatter; passthrough() keeps any extra fields the
// gomarkdoc or hand-authored sources may carry without rejecting
// them at build time. `source` is the repo-relative path of the
// origin file (set by sync-versions) — consumed by the EditLink
// component to deep-link to GitHub.

import { defineCollection, z } from "astro:content";
import { glob } from "astro/loaders";

const docs = defineCollection({
  loader: glob({
    pattern: "**/*.md",
    base: "./src/content/docs",
    //: The default glob `generateId` slugifies every path segment, which
    //: strips the dots out of a release coordinate: "0.1.5/v1/index.md"
    //: collapses to the id "015/v1/index", so Astro publishes that page at
    //: /015/v1/ instead of /0.1.5/v1/. Everything that *links* to a release
    //: — the version dropdown, the per-release redirect (/<release>/ →
    //: /<release>/<major>/), the canonical URLs — uses the DOTTED coordinate
    //: from versions.json ("0.1.5"), so the slugified path 404s and the
    //: dropdown can't resolve the page's own release. Preserve the directory
    //: path verbatim (only drop the ".md") so entry.id === the on-disk
    //: "<release>/<major>/…" coordinate the rest of the site links to.
    generateId: ({ entry }) => entry.replace(/\.md$/, ""),
  }),
  schema: z
    .object({
      title: z.string().optional(),
      description: z.string().optional(),
      updated: z.string().datetime().optional(),
      source: z.string().optional(),
    })
    .passthrough(),
});

export const collections = { docs };
