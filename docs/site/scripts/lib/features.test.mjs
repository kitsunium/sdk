// Unit tests for the curated feature-catalog logic (lib/features.mjs).
// Run via `npm test` (node --test scripts/lib/*.test.mjs).

import { test } from "node:test";
import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  buildFeaturePayload,
  renderCatalogMarkdown,
  deriveProvenance,
  uniqueAnchors,
  DOMAIN_ORDER,
} from "./features.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..", "..", "..", "..");

// A tiny fixture registry + a deterministic provenance map.
const REGISTRY = [
  {
    id: "a",
    domain: "crypto",
    title: "AEAD",
    blurb: "seal/open",
    anchor: "x.go",
    links: { adr: "0013" },
  },
  {
    id: "b",
    domain: "codec",
    title: "Dispatch",
    blurb: "marshal",
    anchor: "y.go",
  },
  {
    id: "c",
    domain: "codec",
    title: "Stream",
    blurb: "streaming",
    anchor: "y.go",
  },
  {
    id: "d",
    domain: "logger",
    title: "Builder",
    blurb: "build",
    anchor: "z.go",
    breaking: true,
  },
];
const PROV = {
  "x.go": { sha: "aaa", short: "aaa", date: "2026-05-30T00:00:00+00:00" },
  "y.go": { sha: "bbb", short: "bbb", date: "2026-04-19T00:00:00+00:00" },
  "z.go": { sha: "ccc", short: "ccc", date: "2026-06-04T00:00:00+00:00" },
};

test("buildFeaturePayload groups by DOMAIN_ORDER", () => {
  const p = buildFeaturePayload(REGISTRY, {
    release: "local",
    major: "v1",
    repoUrl: "u",
    provByAnchor: PROV,
  });
  const order = p.domains.map((d) => d.domain);
  // DOMAIN_ORDER is codec, logger, …, crypto — so logger sorts before crypto
  // regardless of anchor dates.
  assert.deepEqual(order, ["codec", "logger", "crypto"]);
  assert.equal(
    DOMAIN_ORDER.indexOf("logger") < DOMAIN_ORDER.indexOf("crypto"),
    true,
  );
});

test("buildFeaturePayload sorts within a domain newest-first", () => {
  const p = buildFeaturePayload(REGISTRY, {
    release: "local",
    major: "v1",
    repoUrl: "u",
    provByAnchor: PROV,
  });
  // codec has two features sharing the same anchor/date → tiebreak on title.
  const codec = p.domains
    .find((d) => d.domain === "codec")
    .features.map((f) => f.title);
  assert.deepEqual(codec, ["Dispatch", "Stream"]);
});

test("recent is newest-first across domains and respects recentN", () => {
  const p = buildFeaturePayload(REGISTRY, {
    release: "local",
    major: "v1",
    repoUrl: "u",
    provByAnchor: PROV,
    recentN: 2,
  });
  assert.equal(p.recent.length, 2);
  assert.equal(p.recent[0].id, "d"); // 2026-06-04 newest
  assert.equal(p.recent[1].id, "a"); // 2026-05-30 next
});

test("breaking + links flow through; added comes from provenance", () => {
  const p = buildFeaturePayload(REGISTRY, {
    release: "local",
    major: "v1",
    repoUrl: "u",
    provByAnchor: PROV,
  });
  const d = p.recent.find((f) => f.id === "d");
  assert.equal(d.breaking, true);
  assert.equal(d.added, "2026-06-04T00:00:00+00:00");
  const a = p.recent.find((f) => f.id === "a");
  assert.deepEqual(a.links, { adr: "0013" });
});

test("features without provenance are skipped", () => {
  const p = buildFeaturePayload(
    [
      ...REGISTRY,
      {
        id: "e",
        domain: "mac",
        title: "Tag",
        blurb: "tag",
        anchor: "missing.go",
      },
    ],
    { release: "local", major: "v1", repoUrl: "u", provByAnchor: PROV },
  );
  assert.equal(
    p.recent.find((f) => f.id === "e"),
    undefined,
  );
});

test("renderCatalogMarkdown emits domain headers, titles and dates", () => {
  const p = buildFeaturePayload(REGISTRY, {
    release: "local",
    major: "v1",
    repoUrl: "https://h",
    provByAnchor: PROV,
  });
  const md = renderCatalogMarkdown(p);
  assert.match(md, /^## Codec$/m);
  assert.match(md, /\*\*Dispatch\*\* — marshal/);
  assert.match(md, /added 2026-06-04/); // the builder feature day
  assert.match(md, /https:\/\/h\/commit\/ccc/); // anchor commit link
  assert.match(md, /⚠️ breaking/);
});

test("uniqueAnchors dedupes", () => {
  assert.deepEqual(uniqueAnchors(REGISTRY).sort(), ["x.go", "y.go", "z.go"]);
});

// Integration: real git provenance for a known anchor + fail-loud on a bad one.
test("deriveProvenance resolves a real anchor", async () => {
  const prov = await deriveProvenance(
    REPO_ROOT,
    "pkg/v1/codec/codec.go",
    "HEAD",
  );
  assert.match(prov.sha, /^[0-9a-f]{40}$/);
  assert.match(prov.date, /^\d{4}-\d{2}-\d{2}T/);
});

test("deriveProvenance throws on a non-existent anchor", async () => {
  await assert.rejects(
    () =>
      deriveProvenance(REPO_ROOT, "pkg/v1/codec/__does_not_exist__.go", "HEAD"),
    /no creation commit/,
  );
});
