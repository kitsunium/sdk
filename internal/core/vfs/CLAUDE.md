# internal/core/vfs/

## Purpose

Declares the **filesystem port**: `WritableFS` (the four write verbs), its
ADR 0039 capability sibling `AtomicWriter`, their union `FullFS`, the two guards
every implementation runs (`ValidatePath` / `ValidateWritePath`, `ValidatePerm`),
and the eight typed sentinels both filesystems answer with. The 18th core
sibling, admitted by **ADR 0056**.

Reading is **not** declared here. `FS` is a type *alias* of `io/fs.FS`, so the
SDK's answer for reading is the standard library's answer for reading. The
concrete filesystems — memory and operating-system — live in
`internal/service/vfs`.

Code range: `0.2.25.*` (ADR 0056).

## Contents

| File | What lives there |
|---|---|
| `vfs.go` | the package doc and `type FS = fs.FS` — the alias that makes `fs.WalkDir` and `fs.Glob` apply |
| `vfs_interface.go` | `WritableFS`, `AtomicWriter`, `FullFS` |
| `path.go` | `ValidatePath` (the `fs.ValidPath` grammar) and `ValidateWritePath` (plus the root refusal) |
| `perm.go` | `ValidatePerm` — the zero-mode and out-of-`ModePerm` refusals |
| `codes.go` | the eight `Code` constants, `0.2.25.1` … `0.2.25.8` |
| `errors.go` | the eight sentinels, each var named for its `Define` reason |
| `BENCH.md` | guard cost and, more importantly, guard *allocation* |

## Reading is io/fs, and that is a decision, not an omission

`WritableFS` **embeds** `fs.FS`. One line, and every implementation of this port
*is* an `fs.FS`: `fs.WalkDir`, `fs.Glob`, `fs.ReadFile`, `fs.Sub` and every
third-party consumer of `fs.FS` work on it unchanged, with no adapter.

There is deliberately **no** `vfs.Walk` and **no** `vfs.Glob`. Adding one would
not be a feature — the stdlib versions are correct, maintained by the Go
project, and already reachable through the embedded interface. A second pair
would only be a second place for a bug to live, and the day the two disagreed,
the SDK's would be the wrong one.

`TestFSIsTheStdlibTypeUnchanged` pins this: redeclaring `FS` as a look-alike
interface would keep everything inside the SDK compiling and break every stdlib
walker at the boundary.

## The frontier — what the guards do and do not promise

`ValidatePath` is **lexical and nothing more**. It is `fs.ValidPath`, the rule
`io/fs` already imposes on readers, applied to writers so that a name which can
be written can always be read back. It closes `"../../etc/passwd"`.

It does **not** close a symbolic link that leaves the tree. Every element of
`link/secret` is a legal name; whether it escapes depends on what `link` points
at, which is a runtime property of the filesystem and not of the string.
Refusing that is the *implementation's* job — it reports `PathEscaped` — and an
implementation that cannot enforce it MUST refuse to be constructed rather than
accept a name it cannot confine (ADR 0018's shape).

One consequence surprises people and is deliberate: the separator is `/` and
nothing else, so `..\..\etc\passwd` is **accepted** as one legal, peculiar POSIX
filename, and a file written under it lands inside the root. Rejecting
backslashes would make a legal filename unwritable to buy a confinement the
grammar already provides.

## Refusals, never defaults

`ValidatePerm` refuses a zero mode rather than picking one. ADR 0031 admits
clamping where a working default needs no explanation and demands a refusal
where any SDK-chosen value would be arbitrary — a file mode is unambiguously the
second case. `0644` and `0600` differ by *who may read the bytes*, the SDK does
not know what the bytes are, and a mode of `0` is what an unfilled struct field
looks like.

Bits outside `fs.ModePerm` are refused **by name**, so that `0o4755` instead of
`0o755` is a typed error rather than a one-character privilege escalation.

## Conventions

- **No `Public` string here names a path.** A `Public` is read by third parties,
  and a path is the one piece of caller data a filesystem error is guaranteed to
  hold. It travels as a log-only field, reachable through `errs.FieldsOf`.
- **`AtomicWriter` is a sibling, not a fifth method.** ADR 0039: `pkg/v1/vfs`
  aliases these interfaces, Go interfaces are structural, and widening
  `WritableFS` would break every downstream implementation at compile time with
  no deprecation window. `TestWritableFSStaysFrozenAtFiveMethods` and
  `TestAtomicWriterIsASiblingAndNotAMember` are the guards.
- **The two guards are shared, not reimplemented.** Both filesystems call them,
  which is what makes the memory one a faithful double rather than a different
  filesystem with matching method names.

## Do NOT

- **Do NOT add a fifth method to `WritableFS`.** New capability → new sibling
  interface, reached by type assertion. See ADR 0039.
- **Do NOT add `Walk`, `Glob`, `Sub` or any read verb.** They exist in `io/fs`
  and reach this port through the embedded `FS`.
- **Do NOT normalise a path instead of refusing it.** Every normaliser is a
  small parser, every small parser has a case its author missed, and the caller
  never learns that the path it asked for is not the path it got.
- **Do NOT put a path or a mode in a `Public`.** Field, not `Public`.
- **Do NOT "optimise" the refusal paths** by dropping the diagnostic field —
  see `BENCH.md`: the field is the only place the offending value survives.

## Verification

```bash
cd internal/core && GOWORK=off go test -race ./vfs/...
# coverage today: 90.9% (the remainder is refusal branches the service-layer
# conformance suite drives end to end)
cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem ./vfs/
```
