// Shared curated-feature-catalog logic.
//
// Two callers depend on this module:
//   - scripts/sync-versions.mjs — the full docs build (catalog page + banner
//     JSON), run at `npm run prebuild`.
//   - scripts/gen-features.mjs  — a fast, local-release-only regeneration for
//     dev / verification.
//
// The registry (src/data/features.mjs) defines WHAT each feature is; this
// module derives WHEN it landed (from the feature's `anchor` via git) and
// shapes the payload the banner + catalog page consume. No commit parsing —
// provenance is the anchor file's first-appearance commit.

import { execFile as _execFile } from "node:child_process";
import { promisify } from "node:util";

const execFile = promisify(_execFile);

// Render + grouping order for the public domains. A feature whose domain is
// not listed here sorts last (and is flagged by the unit test).
export const DOMAIN_ORDER = [
  "codec",
  "logger",
  "errs",
  "crypto",
  "hash",
  "sign",
  "mac",
  "kdf",
  "agree",
  "password",
];

export const DOMAIN_LABEL = {
  codec: "Codec",
  logger: "Logger",
  errs: "Errors",
  crypto: "Crypto",
  hash: "Hash",
  sign: "Signatures",
  mac: "MAC",
  kdf: "Key derivation",
  agree: "Key agreement",
  password: "Passwords",
};

// deriveProvenance returns { sha, short, date } for the FIRST commit that added
// `anchor`, as seen from `ref` (a tag for a tagged release, or "HEAD" for the
// local release). `--follow` tracks renames so a refactor does not reset the
// date; `--reverse` puts the oldest add first. Throws when the anchor resolves
// to no commit — a fail-loud signal that the registry points at a bad path.
export async function deriveProvenance(repoRoot, anchor, ref = "HEAD") {
  const args = [
    "log",
    ref,
    "--follow",
    "--diff-filter=A",
    "--reverse",
    "--format=%H|%h|%cI",
    "--",
    anchor,
  ];
  let stdout = "";
  try {
    ({ stdout } = await execFile("git", args, { cwd: repoRoot }));
  } catch (err) {
    throw new Error(
      `deriveProvenance: git log failed for ${anchor}: ${err.message ?? err}`,
    );
  }
  const first = stdout
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean)[0];
  if (!first) {
    throw new Error(
      `deriveProvenance: no creation commit for anchor "${anchor}" (ref ${ref}). ` +
        `Fix the anchor in src/data/features.mjs to a file that exists in this history.`,
    );
  }
  const [sha, short, date] = first.split("|");
  return { sha, short, date };
}

// buildFeaturePayload shapes the registry + a provenance map into the payload
// the banner (recent) and catalog page (domains) consume. Pure + sync so it is
// unit-testable with a fake provenance map. `provByAnchor` maps each feature's
// anchor → { sha, short, date }. Features whose anchor has no provenance entry
// are skipped (the generator derives provenance for every anchor first).
export function buildFeaturePayload(
  registry,
  { release, major, repoUrl, provByAnchor, recentN = 5 },
) {
  const enriched = [];
  for (const f of registry) {
    const prov = provByAnchor[f.anchor];
    if (!prov) continue; //: no provenance derived — skip rather than emit a dateless feature.
    enriched.push({
      id: f.id,
      domain: f.domain,
      title: f.title,
      blurb: f.blurb,
      added: prov.date,
      sha: prov.sha,
      short: prov.short,
      breaking: f.breaking === true,
      links: f.links ?? null,
    });
  }

  //: group by domain in DOMAIN_ORDER; within a domain, newest first then title.
  const byDomain = new Map();
  for (const e of enriched) {
    if (!byDomain.has(e.domain)) byDomain.set(e.domain, []);
    byDomain.get(e.domain).push(e);
  }
  const domains = [];
  const seen = new Set();
  const order = [
    ...DOMAIN_ORDER,
    ...[...byDomain.keys()].filter((d) => !DOMAIN_ORDER.includes(d)),
  ];
  for (const d of order) {
    if (seen.has(d)) continue;
    seen.add(d);
    const features = byDomain.get(d);
    if (!features || features.length === 0) continue;
    features.sort((a, b) =>
      a.added < b.added
        ? 1
        : a.added > b.added
          ? -1
          : a.title.localeCompare(b.title),
    );
    domains.push({ domain: d, label: DOMAIN_LABEL[d] ?? d, features });
  }

  //: recent = the most-recently-added capabilities across all domains.
  const recent = [...enriched]
    .sort((a, b) =>
      a.added < b.added
        ? 1
        : a.added > b.added
          ? -1
          : a.title.localeCompare(b.title),
    )
    .slice(0, recentN);

  return {
    release,
    major,
    generatedAt: new Date().toISOString(),
    repoUrl: repoUrl ?? null,
    domains,
    recent,
  };
}

// renderCatalogMarkdown renders the domain-grouped catalog page body. Each
// capability lists its blurb, added-date, the anchor commit, and any ADR link.
export function renderCatalogMarkdown(payload) {
  const lines = [];
  for (const group of payload.domains) {
    lines.push(`## ${group.label}`);
    lines.push("");
    for (const f of group.features) {
      const day = (f.added ?? "").slice(0, 10);
      const commit = payload.repoUrl
        ? `[\`${f.short}\`](${payload.repoUrl}/commit/${f.sha})`
        : `\`${f.short}\``;
      const adr = f.links?.adr ? ` · ADR ${f.links.adr}` : "";
      const breaking = f.breaking ? " · ⚠️ breaking" : "";
      lines.push(
        `- **${f.title}** — ${f.blurb} _(added ${day} · ${commit}${adr}${breaking})_`,
      );
    }
    lines.push("");
  }
  return lines.join("\n");
}

// uniqueAnchors collects the distinct anchors a registry references, so the
// generator derives provenance once per file rather than once per feature.
export function uniqueAnchors(registry) {
  return [...new Set(registry.map((f) => f.anchor))];
}
