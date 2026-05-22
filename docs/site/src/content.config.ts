// docs/site/src/content.config.ts — content collection schema (Astro 5).
//
// The `docs` collection holds the per-major versioned markdown tree
// materialised by docs/site/scripts/sync-versions.mjs into
// src/content/docs/<major>/<release>/. We use the explicit glob
// loader (not the legacy `type: "content"`) so each .md file is
// processed exactly once — the legacy form double-walks the tree
// alongside the glob loader and emits "Duplicate id" warnings for
// every entry on Astro 5.
//
// Schema is intentionally loose: most files we copy in are READMEs
// without frontmatter; passthrough() keeps any extra fields the
// gomarkdoc or hand-authored sources may carry without rejecting
// them at build time.

import { defineCollection, z } from "astro:content";
import { glob } from "astro/loaders";

const docs = defineCollection({
  loader: glob({
    pattern: "**/*.md",
    base: "./src/content/docs",
  }),
  schema: z
    .object({
      title: z.string().optional(),
      description: z.string().optional(),
      updated: z.string().datetime().optional(),
    })
    .passthrough(),
});

export const collections = { docs };
