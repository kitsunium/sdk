// docs/site/scripts/lib/batches.mjs — the pure half of build-site.mjs: which
// releases each build batch holds, and how the outputs that list every page
// (rss.xml, the sitemap) are merged from the batches'. Unit-tested in
// batches.test.mjs.

//: @astrojs/sitemap's own limit of <url> entries per sitemap file.
export const SITEMAP_ENTRY_LIMIT = 45000;

/**
 * Splits the materialised releases into batches of at most `size`.
 *
 * The releases of `first` that exist come first, so the first batch holds the
 * site's default release (the 404 page suggests its pages) and `local`; the
 * others follow in `order` (versions.json's), then any release on disk that
 * `order` does not name, by name.
 *
 * @param {{releases: string[], order?: string[], first?: string[], size: number}} opts
 * @returns {string[][]}
 */
export function batchReleases({ releases, order = [], first = [], size }) {
  if (!Number.isInteger(size) || size < 1) {
    throw new Error(`batch size ${size}: a positive integer`);
  }
  const present = new Set(releases);
  const sequence = [];
  const seen = new Set();
  const add = (r) => {
    if (present.has(r) && !seen.has(r)) {
      seen.add(r);
      sequence.push(r);
    }
  };
  first.forEach(add);
  order.forEach(add);
  [...releases].sort().forEach(add);
  const batches = [];
  for (let i = 0; i < sequence.length; i += size) {
    batches.push(sequence.slice(i, i + size));
  }
  return batches;
}

const ITEM = /<item>[\s\S]*?<\/item>/g;

const ITEM_LINK = /<link>([\s\S]*?)<\/link>/;
const byLink = (a, b) => {
  const la = a.match(ITEM_LINK)?.[1] ?? "";
  const lb = b.match(ITEM_LINK)?.[1] ?? "";
  if (la !== lb) return la < lb ? -1 : 1;
  return a < b ? -1 : a > b ? 1 : 0;
};

/**
 * One rss.xml from the batches': the first's channel, and every batch's items
 * sorted by link — as src/pages/rss.xml.js sorts one build's (an item carries
 * no date, and getCollection's order is not guaranteed), so the same pages
 * give the same bytes, however they were batched.
 *
 * @param {string[]} feeds
 * @returns {string}
 */
export function mergeRss(feeds) {
  if (feeds.length === 0) throw new Error("mergeRss: no feed");
  const [head] = feeds;
  const close = head.lastIndexOf("</channel>");
  const open = head.search(/<item>/);
  if (close < 0) throw new Error("mergeRss: the first feed has no </channel>");
  const prefix = head.slice(0, open >= 0 ? open : close);
  const items = feeds.flatMap((f) => f.match(ITEM) ?? []).sort(byLink);
  return prefix + items.join("") + head.slice(close);
}

const URL_ENTRY = /<url>[\s\S]*?<\/url>/g;
const LOC = /<loc>([\s\S]*?)<\/loc>/;

/**
 * The sitemap files of the merged site from the batches' sitemap-N.xml bodies
 * and the first batch's sitemap-index.xml: every <url>, once per <loc>, in
 * batch order, `limit` per file, and the index listing those files.
 *
 * @param {string} index        the first batch's sitemap-index.xml
 * @param {string[]} sitemaps   every batch's sitemap-N.xml bodies, in order
 * @param {number} [limit]
 * @returns {[string, string][]} [file name, body] pairs, the index included
 */
export function mergeSitemaps(index, sitemaps, limit = SITEMAP_ENTRY_LIMIT) {
  if (sitemaps.length === 0) throw new Error("mergeSitemaps: no sitemap");
  const [head] = sitemaps;
  const open = head.indexOf("<url>");
  const close = head.lastIndexOf("</urlset>");
  if (close < 0) throw new Error("mergeSitemaps: the first sitemap has no </urlset>");
  const prefix = head.slice(0, open >= 0 ? open : close);
  const suffix = head.slice(close);

  const seen = new Set();
  const entries = [];
  for (const body of sitemaps) {
    for (const entry of body.match(URL_ENTRY) ?? []) {
      const loc = entry.match(LOC)?.[1] ?? entry;
      if (seen.has(loc)) continue;
      seen.add(loc);
      entries.push(entry);
    }
  }

  //: the index names its files by absolute URL: keep the first one's prefix.
  const firstLoc = index.match(/<sitemap><loc>([\s\S]*?)sitemap-\d+\.xml<\/loc><\/sitemap>/);
  if (!firstLoc) throw new Error("mergeSitemaps: the sitemap index names no sitemap-N.xml");
  const indexOpen = index.indexOf("<sitemap>");
  const indexClose = index.lastIndexOf("</sitemapindex>");

  const files = [];
  const names = [];
  for (let i = 0; i * limit < entries.length || i === 0; i++) {
    const name = `sitemap-${i}.xml`;
    names.push(name);
    files.push([name, prefix + entries.slice(i * limit, (i + 1) * limit).join("") + suffix]);
  }
  const listed = names
    .map((n) => `<sitemap><loc>${firstLoc[1]}${n}</loc></sitemap>`)
    .join("");
  files.push([
    "sitemap-index.xml",
    index.slice(0, indexOpen) + listed + index.slice(indexClose),
  ]);
  return files;
}
