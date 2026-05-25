// Shared "What's new" / changelog commit logic.
//
// Two callers depend on this module:
//   - scripts/sync-versions.mjs — the full docs build (changelog page +
//     banner JSON), run at `npm run prebuild`.
//   - scripts/gen-whats-new.mjs — a fast, gh-free regeneration used by the
//     pre-commit hook (scripts/pre-commit/gen-whats-new.sh).
//
// Keeping the conventional-commit parsing AND the banner-selection rule here
// means the hook and the build can never disagree on what "What's new" holds.

import { execFile as _execFile } from "node:child_process";
import { promisify } from "node:util";

const execFile = promisify(_execFile);

// One git-log record: full-SHA | short | committer-ISO-date | subject | body,
// terminated by a delimiter that will not occur inside a commit.
export const COMMIT_DELIM = "---COMMIT-END---";
export const COMMIT_FORMAT = `%H|%h|%cI|%s|%b${COMMIT_DELIM}`;

// Conventional-commit subject: type(scope)!: summary. The repo enforces this
// shape (see /workspace/CLAUDE.md), so anything that fails to match is "misc".
export const CONVENTIONAL_RE =
  /^(feat|fix|perf|refactor|chore|docs|test|build|ci|style)(?:\(([^)]+)\))?(!)?:\s*(.+)$/;

// classifyCommit splits a subject into {type, scope, breaking, summary}.
export function classifyCommit(subject) {
  const m = subject.match(CONVENTIONAL_RE);
  if (!m) {
    //: non-conventional subject — bucket as "misc", keep the raw text.
    return { type: "misc", scope: null, breaking: false, summary: subject };
  }
  return {
    type: m[1],
    scope: m[2] ?? null,
    breaking: m[3] === "!",
    summary: m[4],
  };
}

// gitLogCommits runs `git log` in sourceRoot for refSpec (e.g. ["-n","30",
// "HEAD"]) and returns parsed records. Dependency-free + gh-free, so it is
// safe to call from a git hook; a failing git invocation yields [] not a throw.
export async function gitLogCommits(sourceRoot, refSpec) {
  const args = ["log", `--pretty=format:${COMMIT_FORMAT}`, ...refSpec];
  let stdout = "";
  try {
    ({ stdout } = await execFile("git", args, { cwd: sourceRoot }));
  } catch {
    //: empty repo / bad ref — no commits to report, not a crash.
    return [];
  }
  return stdout
    .split(COMMIT_DELIM)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      //: subject/body may contain "|" (piped shell quoted in a commit) —
      //: split only on the first 4 separators to keep the body intact.
      const idx = [];
      let pos = -1;
      for (let i = 0; i < 4; i++) {
        pos = s.indexOf("|", pos + 1);
        if (pos === -1) break;
        idx.push(pos);
      }
      if (idx.length < 4) return null;
      return {
        sha: s.slice(0, idx[0]),
        short: s.slice(idx[0] + 1, idx[1]),
        date: s.slice(idx[1] + 1, idx[2]),
        subject: s.slice(idx[2] + 1, idx[3]),
        body: s.slice(idx[3] + 1),
      };
    })
    .filter(Boolean);
}

// buildWhatsNewPayload selects the banner entries — the most recent 3 feat +
// 2 fix, newest first (commits arrive newest-first from git log) — and wraps
// them with the release header WhatsNew.astro consumes. perf/test/docs/etc.
// never enter the banner (they still appear on the full /changelog page).
export function buildWhatsNewPayload(commits, { release, major, repoUrl }) {
  const tops = [];
  let feat = 0;
  let fix = 0;
  for (const c of commits) {
    const meta = classifyCommit(c.subject);
    if (meta.type === "feat" && feat < 3) {
      //: keep up to 3 feats.
      tops.push({ ...meta, sha: c.sha, short: c.short, date: c.date });
      feat++;
    } else if (meta.type === "fix" && fix < 2) {
      //: keep up to 2 fixes.
      tops.push({ ...meta, sha: c.sha, short: c.short, date: c.date });
      fix++;
    }
    if (feat >= 3 && fix >= 2) break;
  }
  return {
    release,
    major,
    generatedAt: new Date().toISOString(),
    repoUrl: repoUrl ?? null,
    entries: tops,
  };
}
