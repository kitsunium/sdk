// Unit tests for the build batches and the merge of the outputs that list
// every page (lib/batches.mjs). Run via `npm test`.

import { test } from "node:test";
import assert from "node:assert/strict";

import { batchReleases, mergeRss, mergeSitemaps } from "./batches.mjs";

test("the default release and local open the first batch, then versions.json's order", () => {
  assert.deepEqual(
    batchReleases({
      releases: ["0.1.0", "0.2.0", "0.3.0", "local", "0.4.0"],
      order: ["local", "0.4.0", "0.3.0", "0.2.0", "0.1.0"],
      first: ["0.4.0", "local"],
      size: 2,
    }),
    [["0.4.0", "local"], ["0.3.0", "0.2.0"], ["0.1.0"]],
  );
});

test("every release on disk is built once, those versions.json omits last", () => {
  const batches = batchReleases({
    releases: ["b", "a", "local", "0.1.0"],
    order: ["local", "local", "0.1.0", "gone"],
    first: ["missing", "local"],
    size: 3,
  });
  assert.deepEqual(batches, [["local", "0.1.0", "a"], ["b"]]);
});

test("a single batch when the releases fit, none when there is none", () => {
  assert.deepEqual(batchReleases({ releases: ["local"], size: 6 }), [["local"]]);
  assert.deepEqual(batchReleases({ releases: [], size: 6 }), []);
  assert.throws(() => batchReleases({ releases: ["local"], size: 0 }));
});

const feed = (items) =>
  `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>T</title><link>https://x/</link>${items
    .map((i) => `<item><title>${i}</title></item>`)
    .join("")}</channel></rss>`;

const linked = (links) =>
  `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>T</title><link>https://x/</link>${links
    .map((l) => `<item><title>${l}</title><link>https://x/${l}/</link></item>`)
    .join("")}</channel></rss>`;

test("rss: the first channel, every batch's items", () => {
  assert.equal(
    mergeRss([feed(["a", "b"]), feed([]), feed(["c"])]),
    feed(["a", "b", "c"]),
  );
  assert.equal(mergeRss([feed([]), feed(["c"])]), feed(["c"]));
});

test("rss: the items sorted by link, whatever the batches' order", () => {
  const want = linked(["0.1.0/v1", "0.2.0/v1/a", "local/v1", "local/v1/b"]);
  assert.equal(
    mergeRss([linked(["local/v1/b", "local/v1"]), linked(["0.2.0/v1/a", "0.1.0/v1"])]),
    want,
  );
  assert.equal(
    mergeRss([linked(["0.1.0/v1", "local/v1"]), linked(["local/v1/b", "0.2.0/v1/a"])]),
    want,
  );
});

const urlset = (locs) =>
  `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${locs
    .map((l) => `<url><loc>https://x/sdk/${l}</loc></url>`)
    .join("")}</urlset>`;
const index = (n) =>
  `<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${Array.from(
    { length: n },
    (_, i) => `<sitemap><loc>https://x/sdk/sitemap-${i}.xml</loc></sitemap>`,
  ).join("")}</sitemapindex>`;

test("sitemap: every page once, in batch order, and an index naming the files", () => {
  const files = new Map(
    mergeSitemaps(index(1), [urlset(["", "a/", "b/"]), urlset(["", "c/"])]),
  );
  assert.deepEqual([...files.keys()], ["sitemap-0.xml", "sitemap-index.xml"]);
  assert.equal(files.get("sitemap-0.xml"), urlset(["", "a/", "b/", "c/"]));
  assert.equal(files.get("sitemap-index.xml"), index(1));
});

test("sitemap: split at the entry limit, the index lists every file", () => {
  const files = new Map(
    mergeSitemaps(index(1), [urlset(["a/", "b/"]), urlset(["c/"])], 2),
  );
  assert.equal(files.get("sitemap-0.xml"), urlset(["a/", "b/"]));
  assert.equal(files.get("sitemap-1.xml"), urlset(["c/"]));
  assert.equal(files.get("sitemap-index.xml"), index(2));
});
