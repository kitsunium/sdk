// docs/site/scripts/lib/page-catalog.mjs — shared page taxonomy.
//
// The catalog turns a flat list of `getCollection("docs")` entries
// (post-sync-versions materialisation) into the grouped + ordered
// structure both Sidebar.astro and Search.astro need:
//
//     Overview         (Home / Getting started)
//     Packages         (data/codec / errs / observe/logger / …) — formerly "services"
//     Reference        (Concepts / Changelog)
//
// Hidden slugs (adr, contributors) still get pages built but are
// surfaced via the sidebar FOOTER ("For contributors ↗"), not the
// main nav — they're maintainer-facing, not consumer-facing.
//
// Industry rationale (audit recorded in
// /workspace/.claude/contexts/docs-sidebar-rethink.md):
//   - "Philosophy" / "Verification" : 0/7 SDKs reviewed (AWS, Azure,
//     GCP, Cloudflare, Stripe, Go std, Rust std) surface this in the
//     consumer nav — pure maintainer self-indulgence.
//   - "Architecture" : when present, concrete only (HTTP pipeline /
//     security model) → renamed to "Concepts" with concrete content.
//   - ADRs / Benchmarks : 0/7 SDKs surface them in main nav.
//     Benchmarks move INLINE to each package page (cf.
//     .claude/contexts/package-inline-benchmarks.md).

/**
 * Reserved page metadata. Keys match the content-collection slug
 * within the (release, major) prefix.
 *
 * `hidden: true` means the page is materialised AND reachable by URL,
 * but does NOT appear in the main sidebar groups — the Sidebar.astro
 * footer surfaces it via a "For contributors ↗" link instead.
 *
 * @typedef {{ group: string, hint: string, label?: string, order: number, hidden?: boolean }} ReservedMeta
 * @type {Record<string, ReservedMeta>}
 */
export const RESERVED = Object.freeze({
  index: {
    group: "Overview",
    hint: "Project overview",
    label: "Home",
    order: 0,
  },
  "getting-started": {
    group: "Overview",
    hint: "Install + first program",
    label: "Getting started",
    order: 1,
  },
  concepts: {
    group: "Reference",
    hint: "Layer model + error semantics",
    label: "Concepts",
    order: 0,
  },
  changelog: {
    group: "Reference",
    hint: "What changed per release",
    label: "Changelog",
    order: 1,
  },
  //: Hidden — accessible by URL, surfaced via the Sidebar footer link.
  adr: {
    group: "Reference",
    hint: "Decision records",
    label: "ADRs",
    order: 99,
    hidden: true,
  },
  contributors: {
    group: "Reference",
    hint: "Maintainer onboarding",
    label: "For contributors",
    order: 100,
    hidden: true,
  },
});

/**
 * The path a collection id is served at: the id with a trailing `index`
 * segment dropped — "local/v1/index" is "local/v1", "local/v1/adr/index" is
 * "local/v1/adr" —, as the route's getStaticPaths serves it.
 *
 * @param {string} id
 * @returns {string}
 */
export function pagePath(id) {
  return id === "index" ? "" : id.replace(/\/index$/, "");
}

/**
 * Stable presentation order for the top-level buckets.
 * Empty buckets are dropped by buildCatalog().
 */
export const GROUP_ORDER = ["Overview", "Packages", "Reference"];

/**
 * Pure-function projection: collection entries → grouped catalog.
 *
 * @param {Array<{id: string}>} entries  — output of getCollection("docs")
 * @param {string} root                  — "<release>/<major>" (no trailing slash)
 * @param {string} base                  — "/<release>/<major>" (URL prefix)
 * @returns {Array<{ group: string, items: Array<{ href: string, label: string, hint?: string, order: number }> }>}
 */
export function buildCatalog(entries, root, base) {
  const prefix = `${root}/`;
  /** @type {Map<string, Array<{href: string, label: string, hint?: string, order: number}>>} */
  const groups = new Map();
  const push = (g, link) => {
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(link);
  };

  for (const entry of entries) {
    //: the collection keeps a file's path as its id (content.config.ts), so
    //: the landing page of the release is "<root>/index" and a section's
    //: "<root>/adr/index": the route serves each at its directory, and so
    //: does the catalog. Linking "<root>/index/" broke Home on every page.
    const id = pagePath(entry.id);
    if (id !== root && !id.startsWith(prefix)) continue;
    let key, href;
    if (id === root) {
      key = "index";
      href = `${base}/`;
    } else {
      const rest = id.slice(prefix.length);
      //: a reserved section's deeper pages (e.g. adr/0001-foo) are
      //: surfaced by that section's landing page — they stay out of the
      //: catalog. Any other nested id is a package under a family
      //: directory (data/codec — ADR 0155), listed at its full path.
      if (rest.includes("/") && RESERVED[rest.split("/")[0]]) continue;
      key = rest;
      href = `${base}/${key}/`;
    }
    const reserved = RESERVED[key];
    if (reserved) {
      //: Hidden RESERVED entries (ADRs, contributors) are NOT pushed
      //: into the catalog at all — Sidebar.astro renders them via a
      //: separate footer "For contributors ↗" link.
      if (reserved.hidden) continue;
      push(reserved.group, {
        href,
        label: reserved.label ?? key,
        hint: reserved.hint,
        order: reserved.order,
      });
    } else {
      //: everything else is a Go package auto-discovered from a
      //: pkg/<major>/**/README.md (lib/packages.mjs). Label = its path
      //: under pkg/<major>/, so the family reads in the sidebar
      //: ("data/codec") and the alphabetical sort groups each family.
      push("Packages", { href, label: key, hint: "Package", order: 100 });
    }
  }

  return GROUP_ORDER.filter((g) => groups.has(g)).map((g) => ({
    group: g,
    items: groups
      .get(g)
      .sort((a, b) => a.order - b.order || a.label.localeCompare(b.label)),
  }));
}

/**
 * Project a grouped catalog into a flat ordered list. Used by the
 * Previous/Next page footer to find a page's neighbours in the
 * canonical reading order: Overview → Packages → Reference.
 *
 * @param {ReturnType<typeof buildCatalog>} catalog
 * @returns {Array<{ href: string, label: string, group: string }>}
 */
export function flattenCatalog(catalog) {
  const out = [];
  for (const section of catalog) {
    for (const item of section.items) {
      out.push({ href: item.href, label: item.label, group: section.group });
    }
  }
  return out;
}
