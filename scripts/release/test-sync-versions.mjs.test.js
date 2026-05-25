// Node test for docs/site/scripts/sync-versions.mjs (plan A6). The
// sync-versions module exposes pure helpers so the test exercises them
// without touching git or the GitHub API. The end-to-end path is
// covered by the docs build smoke test in CI.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  TAG_REGEX,
  isValidTag,
  parseTag,
  groupByMajor,
  pickLatest,
  buildVersionsJson,
} from "../../docs/site/scripts/lib/tag-format.mjs";

test("TAG_REGEX matches canonical shape", () => {
  assert.match("pkg/v1/v1.32.1", TAG_REGEX);
  assert.match("pkg/v2/v2.0.0-rc.1", TAG_REGEX);
});

test("TAG_REGEX rejects shell-injection bait", () => {
  assert.doesNotMatch("pkg/v1/v1.0.0;rm -rf /", TAG_REGEX);
  assert.doesNotMatch("pkg/v1/v1.0.0 --upload-pack=/evil", TAG_REGEX);
  assert.doesNotMatch("../etc/passwd", TAG_REGEX);
});

test("parseTag extracts the major + semver triple", () => {
  assert.deepEqual(parseTag("pkg/v1/v1.32.1"), {
    major: "v1",
    semver: "1.32.1",
    parts: [1, 32, 1],
    prerelease: null,
  });
  assert.deepEqual(parseTag("pkg/v2/v2.0.0-rc.1"), {
    major: "v2",
    semver: "2.0.0-rc.1",
    parts: [2, 0, 0],
    prerelease: "rc.1",
  });
  assert.equal(parseTag("not-a-tag"), null);
});

test("groupByMajor + pickLatest order patches numerically (not lex)", () => {
  const tags = [
    { tagName: "pkg/v1/v1.0.2", publishedAt: "2026-01-01T00:00:00Z" },
    { tagName: "pkg/v1/v1.0.10", publishedAt: "2026-02-01T00:00:00Z" },
    { tagName: "pkg/v1/v1.0.9", publishedAt: "2026-01-15T00:00:00Z" },
    { tagName: "pkg/v2/v2.0.0", publishedAt: "2026-03-01T00:00:00Z" },
  ];
  const grouped = groupByMajor(tags);
  assert.equal(pickLatest(grouped.v1).tagName, "pkg/v1/v1.0.10");
  assert.equal(pickLatest(grouped.v2).tagName, "pkg/v2/v2.0.0");
});

test("groupByMajor drops drafts, pre-releases, and malformed tags", () => {
  const tags = [
    {
      tagName: "pkg/v1/v1.0.0",
      isDraft: false,
      isPrerelease: false,
      publishedAt: "x",
    },
    {
      tagName: "pkg/v1/v1.0.1-rc.1",
      isDraft: false,
      isPrerelease: true,
      publishedAt: "x",
    },
    {
      tagName: "pkg/v1/draft",
      isDraft: true,
      isPrerelease: false,
      publishedAt: "x",
    },
    { tagName: "bogus", isDraft: false, isPrerelease: false, publishedAt: "x" },
  ];
  const grouped = groupByMajor(tags);
  assert.equal(grouped.v1.length, 1);
  assert.equal(grouped.v1[0].tagName, "pkg/v1/v1.0.0");
});

test("buildVersionsJson marks newest major as default + eol:false", () => {
  const tags = [
    { tagName: "pkg/v1/v1.5.0", publishedAt: "2026-01-01T00:00:00Z" },
    { tagName: "pkg/v2/v2.0.0", publishedAt: "2026-04-01T00:00:00Z" },
  ];
  const out = buildVersionsJson(tags);
  assert.equal(out.length, 2);
  const v2 = out.find((v) => v.major === "v2");
  const v1 = out.find((v) => v.major === "v1");
  assert.equal(v2.default, true);
  assert.equal(v2.eol, false);
  assert.equal(v1.default, false);
  assert.equal(v1.latest, "1.5.0");
});

test("isValidTag is a strict boolean", () => {
  assert.equal(isValidTag("pkg/v1/v1.0.0"), true);
  assert.equal(isValidTag("nope"), false);
});
