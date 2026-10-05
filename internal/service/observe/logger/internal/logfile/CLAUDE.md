<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/observe/logger/internal/logfile/

## Purpose

The **hardened open both file sinks share**: the append-only sink
(`internal/service/observe/logger/sink/file`, `New`) and the rotating one
(`internal/service/observe/logger/writer/rotfile`, `openHardened`, re-run on every reopen after
a rotation). `Open` refuses a path whose final component is a symbolic link
TWICE — by an `os.Lstat` before the open (policy) and by `O_NOFOLLOW` at the
open on every Unix (the TOCTOU window the check leaves) — and opens
`O_APPEND|O_CREATE|O_WRONLY` with the caller's mode. Both refusals name the
indirection the same way (`kind=symlink`, beside `path`), so an operator can
tell a planted link from a full disk on one line (CWE-59).

The two sinks carried copies of all of it — `refuseSymlink`,
`explainOpenFailure`, `kindSymlink`, `open_flags_{unix,other}.go` and the
O_NOFOLLOW proof test — differing only in their error code and wording. Those
two are what this package does **not** own: a sink hands it a `RefusalSpec` with
its two wraps, and every refusal leaves under that sink's code
(`OPEN_FAILED` `0.3.14.*` / `ROT_FILE_OPEN_FAILED`) in that sink's words — byte for
byte what each returned before.

Code range: **none**.

Internal to `internal/service/observe/logger` (Go's `internal/` rule): its two
importers both sit in the logger's subtree, so since ADR 0155 the rule admits
exactly the logger's engines rather than every package of the service layer.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `logfile.go` | `KindSymlink` + `RefusalSpec` + `Open` + `RefuseSymlink` + `ExplainOpenFailure` |
| `open_flags_unix.go` | `openFlags = O_APPEND \| O_CREATE \| O_WRONLY \| O_NOFOLLOW` — every `unix` GOOS, and why the tag is `unix` |
| `open_flags_other.go` | `!unix` fallback (no `O_NOFOLLOW`): windows, plan9, js/wasm, wasip1 |
| `open_flags_unix_internal_test.go` | the proof that the OPEN refuses a link planted after the check — the one copy both sinks used to carry |
| `logfile_external_test.go` | appending, the caller's code on both refusals, the two fields, the target untouched |

## Which platform gets which protection

Protection is **not uniform**, and this table says where it is not. `Lstat` is
`RefuseSymlink`, present everywhere; `O_NOFOLLOW` is the kernel half.

| Platform | `syscall.O_NOFOLLOW` in go1.27.1 | Protection at the open |
|---|---|---|
| every `//go:build unix` GOOS (39 GOOS/GOARCH pairs) | present on all of them | `Lstat` **+** `O_NOFOLLOW` — the TOCTOU window is closed |
| `windows`, `plan9`, `js/wasm` | absent | `Lstat` **only** — a link planted between the check and the open **is followed** |
| `wasip1` | present, but a `path_open` lookupflag, not a kernel flag | `Lstat` only, **by choice** — the refusal would belong to the WASI host |

`open_flags_unix.go` records how that was measured (all 47 pairs of
`go tool dist list`, compiled as a library). The proof test is `//go:build
unix` and runs on real kernels in the `e2e-cross` lane, which lists this package
in `SERVICE_FILE_PKGS` beside the two sinks.

## Conventions

- **`O_NOFOLLOW` governs the FINAL component only.** A link at a parent
  component is traversed; each sink's "do NOT place the log file under an
  attacker-writable directory" pre-condition covers that, not this package.
- **The errno is never consulted.** `O_NOFOLLOW` reports a planted link as
  `ELOOP` (linux, openbsd, darwin), `EMLINK` (freebsd, dragonfly) or `EFTYPE`
  (netbsd); the `Lstat` in `ExplainOpenFailure` is diagnosis, never the decision
  (ADR 0082 §D4).
- **`Open` is the single entry point.** A sink that opens its file any other way
  has a weaker path into it.

## Do NOT

- **Declare an error code here**, or default a `RefusalSpec` field.
- **Add a runtime guard for a missing `O_NOFOLLOW`.** A missing kernel ABI
  constant is split by build tag (ADR 0018 §(c)); a Unix port without it must
  break the build.
- **Claim protection on `!unix`.** Windows needs a different primitive
  (`FILE_FLAG_OPEN_REPARSE_POINT` opens the link and the handle must be
  rejected — ADR 0082 §D2); that is separate work, named as such.

## Verification

```
cd internal/service && GOWORK=off go test -race ./observe/logger/internal/logfile/ ./observe/logger/sink/file/ ./observe/logger/writer/rotfile/
bazel test --config=race //internal/service/observe/logger/internal/logfile:logfile_test
```
