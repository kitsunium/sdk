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

// Two shapes are accepted (ADR 0017):
//   - bare:   pkg/vX.Y.Z         — the public module …/pkg (major 0|1); the
//             docs "major" axis is the SEMVER major ("v0"/"v1").
//   - legacy: pkg/vN/vX.Y.Z      — reserved for a future real …/pkg/vN module
//             (N≥2); the path-major must equal the semver major.
export const TAG_REGEX =
  /^pkg\/(?:v[0-9]+\/)?v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$/;
export const LOCAL_RELEASE = "local";

export function isValidTag(tag) {
  return typeof tag === "string" && TAG_REGEX.test(tag);
}

export function parseTag(tag) {
  if (!isValidTag(tag)) return null;
  const segs = tag.split("/");
  //: bare pkg/vX.Y.Z has 2 segments (no path-major); legacy pkg/vN/vX.Y.Z has 3.
  let major;
  const semverWithV = segs.length === 2 ? segs[1] : segs[2];
  const semver = semverWithV.slice(1);
  const dash = semver.indexOf("-");
  const core = dash === -1 ? semver : semver.slice(0, dash);
  const prerelease = dash === -1 ? null : semver.slice(dash + 1);
  const parts = core.split(".").map(Number);
  if (parts.length !== 3) return null;
  if (segs.length === 2) {
    //: bare module: the grouping major is the SEMVER major (v0 alpha, v1 stable).
    major = `v${parts[0]}`;
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

// buildVersionsJson now emits the two-axis schema described above.
// When a major has no real releases (bootstrap), it gets a single
// "local" release flagged default. Otherwise all real releases land
// in descending semver order, newest flagged default.
export function buildVersionsJson(releases) {
  const grouped = groupByMajor(releases);
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
// dropdown honest once the first pkg/<major>/vX.Y.Z lands.
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
