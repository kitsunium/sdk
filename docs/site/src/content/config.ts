// docs/site/src/content/config.ts — content collection schema
// (plan B5). Astro 5 refuses to build an unschematised collection;
// the `docs` collection here is the bucket the prebuild script fills
// with the per-major markdown tree (`src/content/docs/<major>/`).
//
// Frontmatter is optional everywhere — most files we copy in are
// hand-authored READMEs that omit YAML headers entirely. The schema
// is intentionally loose so any markdown file under /<major>/ loads
// without per-file annotation.

import { z, defineCollection } from "astro:content";

const docs = defineCollection({
  type: "content",
  schema: z.object({
    title: z.string().optional(),
    description: z.string().optional(),
    updated: z.string().datetime().optional(),
  }).passthrough(),
});

export const collections = { docs };
