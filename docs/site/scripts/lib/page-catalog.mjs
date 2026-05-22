// docs/site/scripts/lib/page-catalog.mjs — shared page taxonomy.
//
// The catalog turns a flat list of `getCollection("docs")` entries
// (post-sync-versions materialisation) into the grouped + ordered
// structure both Sidebar.astro and Search.astro need:
//
//     Overview          (Home / Getting started / Philosophy)
//     services          (codec / errs / logger / …)
//     Reference         (Architecture / ADRs / Benchmarks / Verification)
//
// Everything keyed off the page's content collection ID. Any new top-
// level page under src/content/docs/<m>/<r>/ slots in automatically:
//   - reserved keys (index, getting-started, philosophy, architecture,
//     adr, benchmarks, verification) get their fixed group + label.
//   - anything else lands in "services" (the pkg/<major>/<sub>/README
//     pages that sync-versions copies as <sub>.md).
//
// Sub-pages (e.g. adr/0001-foo) are intentionally not surfaced as
// quick-links — the group's landing page surfaces them in-context.

/**
 * Reserved page metadata. Keys match the content-collection slug
 * within the (major, release) prefix.
 *
 * @typedef {{ group: string, hint: string, label?: string, order: number }} ReservedMeta
 * @type {Record<string, ReservedMeta>}
 */
export const RESERVED = Object.freeze({
  index: {
    group: "Overview",
    hint: "Package overview",
    label: "Home",
    order: 0,
  },
  "getting-started": {
    group: "Overview",
    hint: "Install + first program",
    label: "Getting started",
    order: 1,
  },
  philosophy: {
    group: "Overview",
    hint: "SDK-wide rules",
    label: "Philosophy",
    order: 2,
  },
  architecture: {
    group: "Reference",
    hint: "Layer model",
    label: "Architecture",
    order: 0,
  },
  adr: {
    group: "Reference",
    hint: "Decision records",
    label: "ADRs",
    order: 1,
  },
  benchmarks: {
    group: "Reference",
    hint: "Performance reports",
    label: "Benchmarks",
    order: 2,
  },
  verification: {
    group: "Reference",
    hint: "Build / test / lint gates",
    label: "Verification",
    order: 3,
  },
});

/**
 * Stable presentation order for the three top-level buckets.
 * Empty buckets are dropped by buildCatalog().
 */
export const GROUP_ORDER = ["Overview", "services", "Reference"];

/**
 * Pure-function projection: collection entries → grouped catalog.
 *
 * @param {Array<{id: string}>} entries  — output of getCollection("docs")
 * @param {string} root                  — "<major>/<release>" (no trailing slash)
 * @param {string} base                  — "/<major>/<release>" (URL prefix)
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
    if (entry.id !== root && !entry.id.startsWith(prefix)) continue;
    let key, href;
    if (entry.id === root) {
      key = "index";
      href = `${base}/`;
    } else {
      const rest = entry.id.slice(prefix.length);
      //: deeper pages (e.g. adr/0001-foo) are surfaced by their
      //: group's landing page — keep the catalog flat.
      if (rest.includes("/")) continue;
      key = rest;
      href = `${base}/${key}/`;
    }
    const reserved = RESERVED[key];
    if (reserved) {
      push(reserved.group, {
        href,
        label: reserved.label ?? key,
        hint: reserved.hint,
        order: reserved.order,
      });
    } else {
      //: everything else is a service auto-discovered from
      //: pkg/<major>/<sub>/README.md. Label = the directory name.
      push("services", { href, label: key, hint: "Package", order: 100 });
    }
  }

  return GROUP_ORDER.filter((g) => groups.has(g)).map((g) => ({
    group: g,
    items: groups
      .get(g)
      .sort((a, b) => a.order - b.order || a.label.localeCompare(b.label)),
  }));
}
