// docs/site/scripts/lib/tag-format.mjs — JS counterpart of
// scripts/release/lib/tag-format.sh. Same regex, same shape; the two
// files MUST be edited together (see ADR 0007 §1 — tag format is the
// load-bearing contract between the release pipeline and the docs
// site). The Node `--test` suite in scripts/release/ asserts the
// invariants.

export const TAG_REGEX = /^pkg\/v[0-9]+\/v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$/;

export function isValidTag(tag) {
  return typeof tag === "string" && TAG_REGEX.test(tag);
}

// parseTag returns null on malformed input; otherwise an object
// describing every coordinate the rest of the pipeline cares about.
// The numeric `parts` triple is what feeds the version comparator.
export function parseTag(tag) {
  if (!isValidTag(tag)) return null;
  const [, major, semverWithV] = tag.split("/");
  const semver = semverWithV.slice(1); // strip leading "v"
  const dash = semver.indexOf("-");
  const core = dash === -1 ? semver : semver.slice(0, dash);
  const prerelease = dash === -1 ? null : semver.slice(dash + 1);
  const parts = core.split(".").map(Number);
  return { major, semver, parts, prerelease };
}

// groupByMajor filters drafts, pre-releases, and malformed tags, then
// buckets the rest by major. Releases without explicit `isDraft` /
// `isPrerelease` (e.g. local mocks) are treated as published.
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

// pickLatest sorts numerically across the (MAJOR, MINOR, PATCH) triple
// so v1.0.10 beats v1.0.9 (lex would invert them).
export function pickLatest(bucket) {
  return bucket.slice().sort((a, b) => {
    for (let i = 0; i < 3; i++) {
      const d = a.parsed.parts[i] - b.parsed.parts[i];
      if (d !== 0) return d;
    }
    return 0;
  }).at(-1);
}

// buildVersionsJson emits the schema consumed by VersionDropdown +
// the redirect logic: the newest major becomes default:true, every
// entry carries an eol flag (false at first publication; flip to
// true to start the deprecation ritual described in ADR 0007 §5).
export function buildVersionsJson(releases) {
  const grouped = groupByMajor(releases);
  const entries = Object.entries(grouped).map(([major, bucket]) => {
    const latest = pickLatest(bucket);
    return {
      major,
      latest: latest.parsed.semver,
      default: false,
      eol: false,
      publishedAt: latest.publishedAt ?? null,
    };
  });
  // Newest non-EOL major wins the default flag. Tie-break on
  // semver triple so v2 > v1 even if their publish dates invert.
  if (entries.length > 0) {
    const newest = entries
      .filter(e => !e.eol)
      .sort((a, b) => Number(a.major.slice(1)) - Number(b.major.slice(1)))
      .at(-1);
    if (newest) newest.default = true;
  }
  return entries;
}
