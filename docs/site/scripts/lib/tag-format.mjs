// docs/site/scripts/lib/tag-format.mjs — JS counterpart of
// scripts/release/lib/tag-format.sh. Same regex, same shape; the two
// files MUST be edited together (see ADR 0007 §1 — tag format is the
// load-bearing contract between the release pipeline and the docs
// site).
//
// The exported `buildVersionsJson` shapes the data the docs site
// consumes. Schema (v2, two-axis):
//
//   [
//     {
//       "major": "v1",
//       "default": true,
//       "eol":     false,
//       "releases": [
//         { "version": "local", "tag": null, "label": "local",
//           "default": true,  "publishedAt": null },
//         { "version": "1.2.3", "tag": "pkg/v1/v1.2.3", "label": "v1.2.3",
//           "default": false, "publishedAt": "..." }
//       ]
//     },
//     ...
//   ]
//
// "local" is reserved — it represents the working tree / HEAD and is
// only emitted in bootstrap mode (no published releases for the major).

// Three shapes are accepted:
//   - root:   vX.Y.Z             — the SDK module github.com/kitsunium/sdk, at
//             the repository root, one tag per release since ADR 0162; semver
//             major constrained to 0|1 (a bare module path takes no /v1
//             suffix). Its docs "major" axis is the on-disk API directory
//             "v1" (the code lives under pkg/v1/), NOT the semver major —
//             sync-versions uses `major` to select the pkg/<major>/ source
//             directory, and pkg/v0 does not exist.
//   - bare:   pkg/vX.Y.Z         — the public module …/pkg the releases before
//             ADR 0162 were cut as (ADR 0017); same 0|1 major, same "v1" axis.
//             Nothing cuts it any more, and the published ones stay listed.
//   - legacy: pkg/vN/vX.Y.Z      — reserved for a future real …/pkg/vN module
//             (N≥2); the path-major must equal the semver major.
// The vendor modules' tags (third-party/…, framework/connectors/…) are not
// releases of their own: one GitHub release per SDK release lists them.
export const TAG_REGEX =
  /^(?:v[01]|pkg\/(?:v[0-9]+\/v[0-9]+|v[01]))\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$/;
export const LOCAL_RELEASE = "local";

export function isValidTag(tag) {
  return typeof tag === "string" && TAG_REGEX.test(tag);
}

export function parseTag(tag) {
  if (!isValidTag(tag)) return null;
  const segs = tag.split("/");
  //: root vX.Y.Z has 1 segment and bare pkg/vX.Y.Z 2 (no path-major); legacy
  //: pkg/vN/vX.Y.Z has 3.
  let major;
  const semverWithV = segs.at(-1);
  const semver = semverWithV.slice(1);
  const dash = semver.indexOf("-");
  const core = dash === -1 ? semver : semver.slice(0, dash);
  const prerelease = dash === -1 ? null : semver.slice(dash + 1);
  const parts = core.split(".").map(Number);
  if (parts.length !== 3) return null;
  if (segs.length <= 2) {
    //: the SDK module (root) or the bare module …/pkg before it: the grouping
    //: major is the on-disk API directory, which is always "v1" (code lives
    //: under pkg/v1/; there is no pkg/v0). The semver major (0 alpha → 1
    //: stable) lives in `parts`/`semver`, not here.
    major = "v1";
  } else {
    //: legacy/v2+ form: reject tags whose PATH major (pkg/vN) disagrees with the
    //: SEMVER major (e.g. pkg/v2/v1.2.3) — the regex alone accepts them, and a
    //: mismatch silently misgroups releases (ADR 0007 §1: the tag major is
    //: load-bearing). Edited in lockstep with tag-format.sh.
    major = segs[1];
    if (Number(major.slice(1)) !== parts[0]) return null;
  }
  return { major, semver, parts, prerelease };
}

function comparePartsDesc(a, b) {
  for (let i = 0; i < 3; i++) {
    const d = b.parts[i] - a.parts[i];
    if (d !== 0) return d;
  }
  return 0;
}

export function groupByMajor(releases) {
  const out = {};
  for (const r of releases) {
    if (r.isDraft) continue;
    if (r.isPrerelease) continue;
    const parsed = parseTag(r.tagName);
    if (!parsed) continue;
    if (parsed.prerelease) continue;
    (out[parsed.major] ??= []).push({ ...r, parsed });
  }
  return out;
}

export function pickLatest(bucket) {
  return bucket
    .slice()
    .sort((a, b) => {
      for (let i = 0; i < 3; i++) {
        const d = a.parsed.parts[i] - b.parsed.parts[i];
        if (d !== 0) return d;
      }
      return 0;
    })
    .at(-1);
}

// dropTombstones removes a bare pkg/vX.Y.Z whose version a root vX.Y.Z also
// carries: the tombstone the SDK module's first release cut for …/pkg (ADR
// 0162), a go.mod and no package — no release of its own, and no pkg/v1 tree
// to snapshot. `gh release list` never returns it, since it has no GitHub
// release; the `git tag -l` fallback does. Mirrors latest_release_tag in
// scripts/release/lib/tag-format.sh, where the root tag wins the same tie.
export function dropTombstones(releases) {
  const rootVersions = new Set(
    releases
      .filter((r) => isValidTag(r.tagName) && !r.tagName.includes("/"))
      .map((r) => r.tagName.slice(1)),
  );
  return releases.filter((r) => {
    const segs = typeof r.tagName === "string" ? r.tagName.split("/") : [];
    return !(
      segs.length === 2 &&
      segs[0] === "pkg" &&
      rootVersions.has(segs[1].slice(1))
    );
  });
}

// buildVersionsJson now emits the two-axis schema described above.
// When a major has no real releases (bootstrap), it gets a single
// "local" release flagged default. Otherwise all real releases land
// in descending semver order, newest flagged default.
export function buildVersionsJson(releases) {
  const grouped = groupByMajor(dropTombstones(releases));
  const majors = Object.keys(grouped).sort(
    (a, b) => Number(a.slice(1)) - Number(b.slice(1)),
  );

  const entries = majors.map((major) => {
    const sorted = grouped[major]
      .slice()
      .sort((a, b) => comparePartsDesc(a.parsed, b.parsed));
    const releaseList = sorted.map((r, idx) => ({
      version: r.parsed.semver,
      tag: r.tagName,
      label: `v${r.parsed.semver}`,
      default: idx === 0,
      publishedAt: r.publishedAt ?? null,
    }));
    return {
      major,
      default: false,
      eol: false,
      releases: releaseList,
    };
  });

  // Newest non-EOL major becomes the site default. The default release
  // is the newest release within that major.
  if (entries.length > 0) {
    const newestMajor = entries.filter((e) => !e.eol).at(-1);
    if (newestMajor) newestMajor.default = true;
  }
  return entries;
}

// localOnlyVersions returns the bootstrap shape: one entry per major
// directory found on disk, each with a single "local" release. Used
// when no GitHub releases exist yet (fresh clone, first PR, etc.).
export function localOnlyVersions(majors) {
  return majors.map((major, idx) => ({
    major,
    default: idx === majors.length - 1, // newest wins
    eol: false,
    releases: [
      {
        version: LOCAL_RELEASE,
        tag: null,
        label: LOCAL_RELEASE,
        default: true,
        publishedAt: null,
      },
    ],
  }));
}

// stitchVersions merges the on-disk majors (each gets a "local" release) with
// the real tagged releases (buildVersionsJson output), then resolves defaults:
//   - release level: a major's newest real release is default; "local" is
//     default ONLY when the major has no real tagged release yet;
//   - site level: the newest non-EOL major is flagged default.
// Pure (no I/O) so the defaulting policy is unit-testable — the property that
// "local" stops being default the moment a real tag exists is what keeps the
// dropdown honest once a release tag lands.
export function stitchVersions({
  majorsOnDisk,
  realByMajor,
  localRelease = LOCAL_RELEASE,
}) {
  const merged = new Map();
  for (const m of majorsOnDisk) {
    merged.set(m, { major: m, default: false, eol: false, releases: [] });
  }
  for (const entry of realByMajor) {
    //: a tagged major might not exist on disk (rare) — still surface it.
    if (!merged.has(entry.major)) {
      merged.set(entry.major, {
        major: entry.major,
        default: false,
        eol: false,
        releases: [],
      });
    }
    merged.get(entry.major).releases.push(...entry.releases);
  }
  for (const m of majorsOnDisk) {
    const slot = merged.get(m);
    //: "local" is default only while the major has no real release.
    slot.releases.unshift({
      version: localRelease,
      tag: null,
      label: localRelease,
      default: slot.releases.length === 0,
      publishedAt: null,
    });
  }
  const versions = [...merged.values()].sort(
    (a, b) => Number(a.major.slice(1)) - Number(b.major.slice(1)),
  );
  //: newest non-EOL major wins the site-level default.
  if (versions.length > 0) {
    versions.forEach((v) => {
      v.default = false;
    });
    const newest = versions.filter((v) => !v.eol).at(-1);
    if (newest) newest.default = true;
  }
  return versions;
}

// changelogRefSpec returns the `git log` window for a release. "local" windows
// over recent HEAD; a tagged release windows over commits reachable from its
// TAG (a real git ref, e.g. pkg/v1/v1.0.0) — NEVER the bare semver "version",
// which is not a ref. version is for display; tag is for git. Falls back to
// HEAD if a non-local release somehow arrives without a tag.
export function changelogRefSpec(release, tag, localRelease = LOCAL_RELEASE) {
  if (release === localRelease) return ["-n", "30", "HEAD"];
  return ["-n", "100", tag ?? "HEAD"];
}

// findDefaults walks the schema and returns {major, release} the redirect
// logic should target. Falls back to the first major / first release if
// no default flag is set.
export function findDefaults(versions) {
  const m = versions.find((v) => v?.default) ?? versions[0];
  if (!m) return { major: "v1", release: LOCAL_RELEASE };
  const r = m.releases.find((x) => x?.default) ?? m.releases[0];
  return { major: m.major, release: r?.version ?? LOCAL_RELEASE };
}

// selectReleases keeps, in a stitched versions list, the releases `only`
// names (by version: "local", "0.17.0") and drops a major left with none —
// what DOCS_RELEASES asks sync-versions for. The dropdowns then list exactly
// the releases built, so no page links to a release that has no page. A
// major whose default release was dropped defaults to the first one kept;
// the site default moves to the newest major left when its own was dropped.
// An empty or absent `only` keeps everything. Pure, like stitchVersions.
export function selectReleases(versions, only) {
  if (!only || only.length === 0) return versions;
  const keep = new Set(only);
  const kept = versions
    .map((v) => ({
      ...v,
      releases: v.releases.filter((r) => keep.has(r.version)),
    }))
    .filter((v) => v.releases.length > 0)
    .map((v) => ({
      ...v,
      releases: v.releases.some((r) => r.default)
        ? v.releases
        : v.releases.map((r, i) => ({ ...r, default: i === 0 })),
    }));
  if (kept.length > 0 && !kept.some((v) => v.default)) {
    kept.forEach((v) => {
      v.default = false;
    });
    (kept.filter((v) => !v.eol).at(-1) ?? kept.at(-1)).default = true;
  }
  return kept;
}
