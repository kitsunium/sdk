# internal/service/vcs/git/

## Purpose

Implements the `core/vcs` contract by shelling out to the **git binary** (ADR
0076). No VCS library: git's own porcelain is the contract. Stdlib plus
`internal/core/vcs` and `internal/kernel/errs`.

## Contents

| File | Role |
|---|---|
| `exec.go` | the hardened runner — `runGitOutput`, `runGitBlob`, `gitProbe`, `hardenedGitConfig`, `extDiffGuard` |
| `resolve.go` | `Resolve`, the four diff sources it folds together, `shallowState`, `spelledTopLevel` |
| `diff_parse.go` | unified-diff and `--name-status -z` parsing, and `IncludeFunc` |
| `changed_set.go` | `ChangedSetValue`, the concrete `core/vcs.ChangedSet`, and its `key` rewrite |
| `config.go` | `Config` — where the repository is, and the caller's file filter |
| `gitdir.go` | `GitDir`, memoized per root and re-validated on every call |
| `show.go` | `ShowFile` — a blob at a commit, or the two refusals |
| `head.go` | `Head` + `HeadValue` — HEAD's commit, its committer date, tracked changes; `committerTime` reads the raw commit object (ADR 0100) |

## Why-this-shape

### Running against a repository you do not control

`.git/config` travels with a clone, and several of its keys make git execute a
command the repository chose. `hardenedGitConfig` neutralises them, and the file
documents what was **demonstrated** rather than assumed — against a repository
with the key set to a script, this package's own invocations executed it 5
times before the guard and 0 after:

- `core.fsmonitor` — git runs it to enumerate changed paths, on status and diff.
  Disabled with `-c`, which beats every config file, so the target repository
  cannot re-enable it.
- `diff.external` — git runs it instead of its own diff. This one is NOT
  disabled with `-c`: setting it empty makes git try to execute `""` and abort,
  breaking diff outright. The documented lever is `--no-ext-diff`, injected
  per-subcommand by `extDiffGuard` because other subcommands reject the flag.
- `core.hooksPath` — no subcommand here runs a hook, and it is set anyway so the
  guarantee does not depend on that list staying read-only.

A third group executes nothing and was found later, by asking what a hostile
`.git/config` could do to the PARSER rather than to the process. `diff.srcPrefix`
/ `diff.dstPrefix`, `diff.mnemonicPrefix` and `diff.noprefix` choose the `a/`
and `b/` prefixes of a unified-diff header. The `b/` strip then leaves the
repository's own prefix in place, the line ranges are filed under
`<root>/DST/x.go` or `<root>/w/x.go`, and the file answers false for every line
it changed. Measured against a repository carrying each key:

| key | `ContainsFile` | `ContainsLine` |
|---|---|---|
| `diff.srcPrefix=SRC/` + `diff.dstPrefix=DST/` | true | **false** |
| `diff.mnemonicPrefix=true` (index / working tree) | true | **false** |
| `diff.noprefix=true` | true | true |

`ContainsFile` survives all three because the NUL-separated name-status pass
carries no prefixes at all — the two-pass design doing exactly what it exists
for, and also why nothing else looked wrong. `diff.noprefix` survives by
coincidence (`x.go` passes the `b/` strip unchanged) and is pinned anyway,
because it OVERRIDES the two explicit prefixes: neutralising them without it
changes nothing. All four are set to git's documented defaults rather than
disabled, since `a/` and `b/` are what the parser is written against.

Deliberately NOT hardened, to avoid hardening against nothing: `core.pager`
(tested — git detects the non-TTY and skips paging), `diff.<driver>.textconv`
(did not fire on any invocation this package makes), and the network-only keys
(`credential.helper`, `core.sshCommand`, `protocol.*`), since nothing here talks
to a remote.

### Two spellings of one root, and only two

`git rev-parse --show-toplevel` canonicalises: point it at a symbolic link and
it answers with the link's target. Every recorded path is built from that, and
`Contains*` compares lexically, so a caller that reached the repository through
a link — a CI checkout, a `/tmp` that is a link on macOS, a worktree behind a
convenience symlink — got a resolution that was NOT degraded, was NOT empty, and
answered false to every question it asked. That is the silent under-report this
package exists to refuse, arriving through the one path a caller does not
choose.

`spelledTopLevel` derives the caller's own spelling of the top level once, with
the only `EvalSymlinks` calls this package performs (one on the hint, one on the
canonical root), and only when the hint is not already inside the canonical
root — so the ordinary case pays nothing. Every
entry is then recorded under BOTH spellings while the set is being built, by
`ChangedSetValue.spelledAs`.

Build time, not query time, and the asymmetry is the argument: the alias is
known once, at construction, while a caller asks `ContainsLine` once per
diagnostic. Mirroring on the way in leaves the read path byte-identical to what
`BENCH.md` measured — one map lookup, no branch on the alias, zero allocations —
where a per-query rewrite would have added a `filepath.Join` allocation to every
query on an aliased set. An indirection anywhere else in a queried path is still
lexical and still does not match; that is stated in the port comment rather than
fixed, because a caller that builds its own paths chooses those spellings.

| | aliased set | unaliased set |
|---|---|---|
| cost at build | one extra map entry per file, dir and range | none |
| cost per query | none | none |

On **Windows** the root is a spelling the caller did not choose even without a
link, and the first run of the suite there found it (ADR 0095). git prints
`--show-toplevel` `/`-separated and with 8.3 short names expanded
(`C:/Users/runneradmin/…`), while a CI runner's `%TEMP%` — and so `t.TempDir()`
— is spelled short (`C:\Users\RUNNER~1\…`). The root was kept in git's form and
every entry went through `filepath.Join`, so `spelledAs`'s prefix match compared
`C:/…` with `C:\…`, never matched, and every query through the caller's own
spelling answered false on a set that was neither degraded nor empty — the
under-report this section exists to refuse. `repoTopLevel` now puts git's answer
in the OS's form (`filepath.Clean`) before anything is keyed on it, and
`spelledTopLevel` resolves the canonical root as well as the hint before
comparing them, because what git resolves (a short name, a link) is the git
build's business and the two must meet at the same place.
`TestResolveAnswersUnderTheTemporaryDirectorysOwnSpelling` queries through the
temporary directory's own spelling and its resolved one on every lane: the 8.3
case on Windows, the `/var` link on macOS.

### A probe that did not answer is not an answer

`shallowState` returns two booleans, and the second is the point. `git rev-parse
--is-shallow-repository` arrived in git 2.15; an older git fails the invocation,
and any git could answer something we cannot read. Folding either into "not
shallow" scopes a diff against history that was truncated — so `known=false`
degrades, and the Reason names the probe.

### Never a silent empty diff

Every failure path in `Resolve` produces `FullFallback` with a Reason. A partial
changed set would under-report, and under-reporting is the one answer that lets a
caller skip code it should have examined. A git invocation that fails or times
out mid-collection discards what it had and degrades.

### Membership is resolved twice, on purpose

`addDiff` runs the `--name-status -z` pass FIRST and the unified diff second. The
`-z` format is NUL-separated and never c-quoted, so a path with a tab, a newline
or non-ASCII bytes arrives verbatim; the unified-diff header for that same file
may be unresolvable. The first pass is therefore authoritative for membership and
the second only adds line ranges — a file the parser cannot header-match is still
file-touched, never dropped.

### The file filter is the caller's

`IncludeFunc` replaces what the source implementation hard-coded: a `.go` suffix
test plus a generated-file probe. Those are one caller's policy — a changed set
over Markdown is just as legitimate — so the caller states it. A nil filter
admits everything, which is the safe direction per ADR 0031: including too much
widens a scope, while a default that dropped files would under-report.

### The memo is re-validated, not merely stored

`GitDir` still pays one `git rev-parse --git-dir` per root, and still never
caches a failure. What it also does is check, on every call, that the answer is
still true: the `.git` entry at the root is the same file it was — or is still
absent — and the resolved directory is still the same directory. Two `os.Lstat`
calls, ~2.9 µs against the ~2.7 ms subprocess they stand in for, measured here.

That pair catches a repository that MOVED, one deleted and re-created, and a
root that becomes its own repository under a parent one. The last of those is
why the check is two lstats and not one: the ADR's original claim — that a root
becoming a repository later is picked up because failures are not cached — holds
only when the first call FAILED, and a root inside a parent repository resolves
successfully. What is still not caught is a repository created at a directory
BETWEEN the root and the worktree top level; only rev-parse's own discovery walk
sees that, and the doc comment says so.

### `ShowFile` has two refusals and they mean different things

`PathAbsent` says the commit is readable and holds no object at that path, which
is what tells "deleted" from "emptied" — an empty file is a successful read of
`""`. `CommandFailed` says anything else. The classification runs only on the
failure path, and it is two `cat-file -e` probes rather than one, because
`cat-file` answers "no such object" identically for a commit that does not
resolve and for a path that is not in it: without the commit probe an
unreachable SHA would be reported as a missing file, and a caller would retry
with other paths forever. git's stderr is never parsed — its wording is
localised and unversioned.

### `Head` reads three facts and trusts none of them to `git log`

`Head` answers what a build description asks of a local module — the commit,
its time, whether the tree is modified — in three hardened invocations:
`rev-parse --verify HEAD^{commit}`, `cat-file commit <sha>`, and
`--no-optional-locks status --porcelain --untracked-files=no`.

- **The time comes from the raw commit object.** `git log --format=%cI` is the
  obvious spelling and the wrong one here: `log` honours `log.showSignature`,
  and a hostile `.git/config` pairing it with `gpg.program` hands the planted
  program a signature to "verify". `cat-file commit` prints bytes and verifies
  nothing; `committerTime` reads the committer line's last two fields after the
  email's closing bracket, in the offset they were recorded with — what `%cI`
  would have printed.
- **`status` is where `core.fsmonitor` fires.** Measured against a planted
  payload: a raw `git status` executed it twice, the hardened one never —
  `TestHeadRefusesRepositoryControlledExecution`.
- **`status` also runs filters.** A tracked file whose stat changed goes
  through its clean (or long-running process) filter, a command a
  `.git/config` names. `filterGuard` lists the configured drivers — reading
  the configuration runs nothing — and empties each one's `clean` and
  `process` with `-c`, which git reads as no filter; a driver name holding `=`
  cannot be addressed by `-c` and is refused. Measured: without the guard the
  planted filter ran, with it never — `TestHeadRunsNoFilterTheRepositoryConfigures`.
- **A cancelled context is not "no repository".** A failed `rev-parse` under a
  context already done is returned as it is, never probed on that context.
- **`--no-optional-locks`** keeps a read-only question from taking the index
  lock a concurrent `git commit` in the same tree needs.
- **A status that fails is an error, not "clean".** "Clean" is a claim git
  refused to make.
- **Modified counts tracked files.** An untracked file is not part of what the
  commit describes; Go's own `vcs.modified` counts it, and the facade says so.
- **What the hardening does NOT cover.** A clean filter planted in
  `.git/config` and bound by `.gitattributes` runs whenever git must re-hash a
  tracked file whose size did not change — measured once during the hardened
  `status`, seven times during a `diff`. That is the same exposure `Resolve`'s
  diff already has; closing it needs the driver names before the invocation and
  belongs to the shared runner (ADR 0100 §Deferred).

## Do NOT

- Add a git invocation that bypasses `runGitOutput`. The hardening is applied
  there, once.
- Return a partial `ChangedSetValue` from a failed collection. Degrade.
- Reintroduce a suffix or generated-file test in this package. It belongs to the
  caller, and `Test_parseNameStatus` pins both directions.
- Read a decision out of git's stderr. It is localised, and a refusal that
  depends on the operator's language is a refusal that changes under `LC_ALL`.
- Add `--find-copies-harder`. `-C` alone only offers a copy whose SOURCE was
  modified in the same changeset, so the source always carries its own record
  and the copy marking adds nothing — which is the whole of why ADR 0076
  §Deferred's copy entry closes without a code change.
  `TestACopysSourceIsAlreadyInTheSetForItsOwnReason` pins the record shapes that
  reasoning rests on.

## Verification

```sh
bazel test --config=race //internal/service/vcs/git:git_test
go test -race ./internal/service/vcs/git/...
```
