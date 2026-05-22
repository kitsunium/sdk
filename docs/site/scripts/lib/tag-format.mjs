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

export const TAG_REGEX =
  /^pkg\/v[0-9]+\/v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$/;
export const LOCAL_RELEASE = "local";

export function isValidTag(tag) {
  return typeof tag === "string" && TAG_REGEX.test(tag);
}

export function parseTag(tag) {
  if (!isValidTag(tag)) return null;
  const [, major, semverWithV] = tag.split("/");
  const semver = semverWithV.slice(1);
  const dash = semver.indexOf("-");
  const core = dash === -1 ? semver : semver.slice(0, dash);
  const prerelease = dash === -1 ? null : semver.slice(dash + 1);
  const parts = core.split(".").map(Number);
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

// findDefaults walks the schema and returns {major, release} the redirect
// logic should target. Falls back to the first major / first release if
// no default flag is set.
export function findDefaults(versions) {
  const m = versions.find((v) => v?.default) ?? versions[0];
  if (!m) return { major: "v1", release: LOCAL_RELEASE };
  const r = m.releases.find((x) => x?.default) ?? m.releases[0];
  return { major: m.major, release: r?.version ?? LOCAL_RELEASE };
}
