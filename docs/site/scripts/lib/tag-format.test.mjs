// Tests for the docs versioning policy (run via `npm test` → node:test).
// These guard the two properties that keep the version dropdown honest once
// the first release tag lands, plus the tag-vs-version git-ref contract.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildVersionsJson,
  parseTag,
  stitchVersions,
  changelogRefSpec,
  LOCAL_RELEASE,
} from "./tag-format.mjs";

// rel locates a release entry by version within a major.
const rel = (versions, major, version) =>
  versions
    .find((v) => v.major === major)
    .releases.find((r) => r.version === version);

// A gh-release-list-shaped record for a tag.
const ghRelease = (tagName, publishedAt = null) => ({
  tagName,
  publishedAt,
  isDraft: false,
  isPrerelease: false,
});

test("local is the default release ONLY when the major has no real release", () => {
  const versions = stitchVersions({ majorsOnDisk: ["v1"], realByMajor: [] });
  const v1 = versions.find((v) => v.major === "v1");
  assert.equal(v1.releases.length, 1);
  assert.equal(v1.releases[0].version, LOCAL_RELEASE);
  assert.equal(
    v1.releases[0].default,
    true,
    "local must be default while no tag exists",
  );
});

test("once pkg/v1/v1.0.0 exists, v1.0.0 becomes default and local does not", () => {
  const realByMajor = buildVersionsJson([
    ghRelease("pkg/v1/v1.0.0", "2026-01-01T00:00:00Z"),
  ]);
  const versions = stitchVersions({ majorsOnDisk: ["v1"], realByMajor });
  assert.equal(
    rel(versions, "v1", LOCAL_RELEASE).default,
    false,
    "local must NOT be default once a tag exists",
  );
  assert.equal(
    rel(versions, "v1", "1.0.0").default,
    true,
    "the real release must be default",
  );
});

test("the newest tag wins the default among multiple releases", () => {
  const realByMajor = buildVersionsJson([
    ghRelease("pkg/v1/v1.0.0", "2026-01-01T00:00:00Z"),
    ghRelease("pkg/v1/v1.1.0", "2026-02-01T00:00:00Z"),
  ]);
  const versions = stitchVersions({ majorsOnDisk: ["v1"], realByMajor });
  assert.equal(rel(versions, "v1", "1.1.0").default, true);
  assert.equal(rel(versions, "v1", "1.0.0").default, false);
  assert.equal(rel(versions, "v1", LOCAL_RELEASE).default, false);
});

test("changelogRefSpec talks to git via the TAG, never the bare version", () => {
  //: local windows over recent HEAD.
  assert.deepEqual(changelogRefSpec(LOCAL_RELEASE, null), ["-n", "30", "HEAD"]);
  //: a tagged release windows over the full tag ref, not the semver "1.0.0".
  const spec = changelogRefSpec("1.0.0", "pkg/v1/v1.0.0");
  assert.deepEqual(spec, ["-n", "100", "pkg/v1/v1.0.0"]);
  assert.ok(
    !spec.includes("1.0.0"),
    "must not pass the bare version as a git ref",
  );
  assert.ok(spec.includes("pkg/v1/v1.0.0"), "must pass the full tag ref");
});

test("a real release keeps version for display and tag for git", () => {
  const realByMajor = buildVersionsJson([ghRelease("pkg/v1/v1.0.0")]);
  const r = rel(
    stitchVersions({ majorsOnDisk: ["v1"], realByMajor }),
    "v1",
    "1.0.0",
  );
  assert.equal(r.version, "1.0.0", "version is the display semver");
  assert.equal(r.tag, "pkg/v1/v1.0.0", "tag is the git ref");
  assert.equal(r.label, "v1.0.0");
});

test("parseTag rejects path-major ≠ semver-major (pkg/v1/v2.0.0)", () => {
  //: shape-valid but the directory major (v1) disagrees with the semver
  //: major (v2) — must be rejected so releases never misgroup.
  assert.equal(
    parseTag("pkg/v1/v2.0.0"),
    null,
    "mismatched major must be null",
  );
  assert.equal(
    parseTag("pkg/v2/v1.2.3"),
    null,
    "mismatched major must be null",
  );
  //: aligned majors still parse.
  const ok = parseTag("pkg/v2/v2.1.0");
  assert.equal(ok?.major, "v2");
  assert.deepEqual(ok?.parts, [2, 1, 0]);
});

test("parseTag accepts the bare pkg/vX.Y.Z shape (ADR 0017)", () => {
  //: the public module is …/pkg (no path-major). The docs grouping major is
  //: ALWAYS the on-disk API directory "v1" (code lives under pkg/v1/); the
  //: semver major (0 alpha → 1 stable) lives in parts/semver, not in `major`.
  const alpha = parseTag("pkg/v0.1.0");
  assert.equal(alpha?.major, "v1");
  assert.deepEqual(alpha?.parts, [0, 1, 0]);
  assert.equal(alpha?.semver, "0.1.0");
  assert.equal(alpha?.prerelease, null);

  const stable = parseTag("pkg/v1.0.0");
  assert.equal(stable?.major, "v1");
  assert.deepEqual(stable?.parts, [1, 0, 0]);

  const pre = parseTag("pkg/v0.2.0-rc.1");
  assert.equal(pre?.major, "v1");
  assert.equal(pre?.semver, "0.2.0-rc.1");
  assert.equal(pre?.prerelease, "rc.1");

  //: bare v2+ is NOT a valid bare tag (a real v2 needs the …/pkg/v2 module and
  //: the legacy pkg/v2/v2.0.0 shape); reject it here too.
  assert.equal(parseTag("pkg/v2.0.0"), null);

  //: garbage after the semver is still rejected (anchored regex).
  assert.equal(parseTag("pkg/v0.1.0;rm -rf /"), null);
});
