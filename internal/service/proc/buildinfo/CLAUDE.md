# internal/service/proc/buildinfo/

## Purpose

What the running binary was BUILT from (ADR 0100, ADR 0172): the build
information the Go toolchain embeds, read into a main module and its
dependencies. Public facades: `pkg/v1/proc/buildinfo` (the reading alone) and
`pkg/v1/proc/process` (beside spawning and `Self`), both aliasing the same
values.

It was `internal/service/proc/self`'s `build.go` until ADR 0172 split it out:
`self` imports `runtime/metrics`, and Go runs the initialisation of every
package a binary imports — so a program asking only what it was built from
(the framework's `kit`, in every product) paid for the runtime's metric table
at every start. This package imports the standard library and
`internal/kernel/semver` only.

`internal/kernel/semver` recognises a pseudo-version and reads its revision
and time — the kernel primitive that replaced `golang.org/x/mod/module` here
(ADR 0156 §4), so this package reaches no module outside the SDK.

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `build.go` | `BuildValue` + `Module(path)`, `ModuleValue`, `ReadBuild`, `ParseBuild`, and the three readings of a recorded version (`fromVersion`, `mainModule`, `dependency`) |

## Why-this-shape

- **A package of its own, not a file of `self`** (ADR 0172). What it imports
  is what every reader of a build initialises; `runtime/metrics` alone was
  0.031 ms of a 0.72 ms start-up initialisation, measured with
  `GODEBUG=inittrace=1` on the framework's smallest product.
- **No error anywhere.** A binary built without module support is a `false`,
  not a fault a caller could act on.
- **A recorded version conflates three things; `ModuleValue` separates them.**
  A RELEASE (`Version`), a COMMIT (`Revision`, `Time` — what a pseudo-version
  and the main module's `vcs.*` stamp name), and a DIRECTORY (`Local`, `Dir` —
  a directory replacement, a workspace module, a main module built in its own
  tree). The downstream copy this replaces reported a pseudo-version as "no
  version" and lost the commit it named, and left a workspace module neither
  versioned nor local; both are now told. The main module's stamp beats its
  pseudo-version (a full revision against a twelve-character prefix, exact time
  against a second-granular copy), and `+dirty` — which Go 1.24 appends —
  becomes `Modified` instead of staying in the version string. A pseudo-version
  whose stamp is no instant (a thirteenth month) is still a commit: its
  revision is kept and its time left zero, never guessed.
- **Measured, not assumed: what the toolchain records.** In workspace mode a
  directory replacement is recorded relative to the WORKSPACE root, not to the
  main module (`../lib` in `go.mod` read back as `./lib`), and a workspace
  module is recorded as `(devel)` with no directory at all. `Dir`'s comment
  says both.
## Do NOT

- Import `runtime/metrics`, the spawn engine, or anything a program that only
  reads its build would initialise for nothing (ADR 0172).
- Parse pseudo-versions here. `kernel/semver` owns the grammar — the three
  shapes and their build metadata — and its suite holds it to the toolchain's
  own pattern on every near miss; a second reading in this package would be
  the hand-written pattern ADR 0100 refused.

## Verification

```sh
bazel test --config=race //internal/service/proc/buildinfo:buildinfo_test
go test -race ./internal/service/proc/buildinfo/
```
