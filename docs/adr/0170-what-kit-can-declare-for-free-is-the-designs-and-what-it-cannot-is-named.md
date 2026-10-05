# ADR 0170 — what kit can declare for free is the design's, and what it cannot is named

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 8 of "kit regenerates the Go SDK from its design" — raise the share of the exported surface kit writes, without losing any performance)
- **Amends**: [ADR 0168](0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md) §2 (a wrapper that inlines is not yet free: its machine code is measured) and §3 (`kernel/backoff`'s `Value` and the kernel's other types are now the design's); [ADR 0169](0169-the-sdk-is-rebuilt-from-its-design-in-a-copy-and-a-generation-is-held-to-the-whole-inline-set.md) §Measured (the share kit writes: 45.0 % then, 56.6 % now) and §1 (kit `v0.1.0-rc.8`)
- **Related**: [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md), [ADR 0166](0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md), [ADR 0167](0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md)

## Context

ADR 0168 gave the design three members — `decl: true`, `impl:`,
`implements:` — and used them on two kernel packages; ADR 0169 held a
generation to the project's inline set and counted what kit writes: 3 241 of
7 208 exported declarations (45.0 %), 12 of them declarations. Every other
type, function and method of `internal/kernel`, `internal/core`,
`internal/service`, the framework and the hand-written half of `pkg/v1` was
still declared by hand and held to the design by the pins.

A type's declaration costs nothing at run time. A wrapper costs nothing only
when the compiler says so, and this change found that "it inlines", the test
ADR 0168 and ADR 0169 apply, is not what the compiler says: it is necessary,
not sufficient.

## Decision

### 1. Every exported type declared alone is the design's

632 types of 130 packages become `decl: true`, each struct with every field,
unexported ones included, and 22 compliance assertions become their type's
`implements:`; kit gen writes them into each package's `decl_gen.go` and the
hand-written declarations are gone, with the 107 files they left empty
(rule 5). Methods, constructors and every body stay hand-written.

`kit design import -decls` refuses a package with no wrapper
(`DECLS_UNSHAPED`), although its types alone are declarable. The import
reads a package as shaped once the design declares anything of it, so the
conversion marked one type of each package `decl: true` in the design and
let the import read the rest — every exported type, every field — from the
code.

What stays hand-declared:

- **a type in a parenthesised declaration** (`kernel/errs`' four) or under a
  build constraint (two in `internal/service`): kit writes neither;
- **an interface with an unexported method** — `framework/internal/kit`'s 21
  sealed configurer interfaces: the design lists no unexported method;
- **a type whose doc carries a directive** — the model's `NodeKind`,
  `EdgeKind` and `EventType` are `//ktn:wire-format`, and the doc kit writes
  from the design does not keep the marker;
- **a struct with a field whose type nothing else names** —
  `core/observe/logger.Value`, `writer.Spec`, `core/security/secret.Value`,
  `mail/spool.Spool`, `net/websocket.Conn`, `framework/internal/kit.Binary`
  and the model's `MailMessage`. ktn-linter's `KTN-STRUCT-COLOCATE` wants
  such a type beside its struct, skips `decl_gen.go` as generated, and
  honours no `exclude:` (measured: a `"**"` pattern changes nothing; only
  `enabled: false` silences it);
- **three assertions** — `framework/internal/kit`'s `Routine` and `Watch`
  implement `starter`, and `Frontend` `mounter`, through an unexported method
  with a pointer receiver; kit chooses the form from the receivers the design
  lists, all values, and writes `T{}`, which does not compile.

### 2. A wrapper is kept only where the machine code is the function's

An exported function or method becomes a wrapper over an unexported body
named after it (`Major` → `major`) when all of this holds:

1. **kit accepts it** (ADR 0168 §2, ADR 0169 §1): the body inlines into the
   wrapper, the wrapper inlines, every `pkg/v1` forwarder of it still
   inlines, and no function of the SDK stops inlining. Candidates were the
   bodies costing at most 76 — a wrapper costs its body plus 2 to 15, not a
   constant 4 —, not generic, not under a build constraint, every parameter
   named, no directive in the doc, and a lowered name that is free: not
   predeclared or a keyword, not a package-scope or import name, not a field
   or a method of the receiver at any depth (`types.LookupFieldOrMethod`, so
   a promoted method is never shadowed), not one of the function's own
   parameters or named results. kit refused 72 more: wrappers that would not
   inline (`concur/group.NewJoined` 89, `errs.CodeOf` 88,
   `secret.Keyring.Seal` 88, `ipc.Dialer.Dial` 84), forwarders that would stop
   inlining (`pkg/v1/errs.PublicOf` 81), and callers that would
   (`backoff.NormalMultiplier` through `Grow`, `Widen` and
   `resilience.NewRetry`). The guard names the function that lost; the
   wrapper behind it was found in the compiler's inlining report.
2. **its cost stays where it was against 20**: past 5 000 nodes a caller
   inlines only a callee of cost 20 or less, and the guard compares
   inlinability, not cost. 17 wrappers crossed 20 (`slogbridge.NewHandler` 14
   → 27) and keep their bodies.
3. **the machine code is the same.** Every SDK function was compiled for
   linux/amd64 with `-S` before any wrapper and after, and compared function
   by function — offsets, positions, `PCDATA`/`FUNCDATA`, inline marks,
   jump targets, closure hashes and temporaries' numbers masked, a body
   compared with the function it was. 251 of 456 accepted wrappers changed
   code:
   - **a body with more than one `return`** is inlined into its wrapper as
     assignments and a jump, so the wrapper is not the function: 65 of 69;
   - **one more level of inlining** reorders registers and stack slots in
     the callers (`errs.Code.String` and `Padded` over `Major`, `Layer` and
     `Serial`), and grew 114 frames on the first pass;
   - **a generic method stopped inlining** — kit's guard skips
     instantiations — through `model.TableName`: the shaped
     `StoreService.table` went from 80 to 86, a call more on every store;
   - **a closure is compiled once more** when the wrapper inlines a body that
     builds one: 42 of `framework/kit`'s forwarders, and kit's `Anyone` and
     `AnyUser` over `accessMarker`;
   - **a promoted method changed** — `toml.LocalDate.AsTime` in
     `LocalDateTime`.

   They keep their bodies, under their own names. **205 wrappers** remain: 6
   in the kernel, 76 in the core, 51 in the service, 68 in the framework and 4
   in `pkg/v1`. Each body carries the doc `<impl> is <F>'s body: decl_gen.go
   writes <F>, from the design, as one call of it.`, plus any
   `IFACE-PLUGIN:`/`IFACE-OPAQUE:` marker its old doc carried. A constructor
   of a struct that stays hand-declared stays whole: `KTN-STRUCT-CTOR` does
   not see a constructor in `decl_gen.go`.

### 3. kit `v0.1.0-rc.8`: a generation reads the declarations it writes

A pin reads its type from the code kit gen loaded, and a type defined over
another package's struct is pinned with that struct's fields. When the same
generation writes that struct's `decl_gen.go` for the first time, the load
had not seen it, and the pin read `invalid type` until a second `kit gen` —
so `make from-zero`, which deletes every generated file and runs kit gen
once, failed: `framework/kit/api_gen_test.go: cannot use type … Delay outside
a type constraint`. kit rc.8 (kitsunium/platform `feat/kit-gen-fixpoint`)
renders and writes a second time when a generation moves a `decl_gen.go`; the
declarations being the design's alone, the second pass is a fixpoint.
`design/sdk.yaml` pins it.

## Measured

- **The same machine code.** Against `main` (`fd54e5dd`), 40 427 functions:
  every one has the same instructions and the same frame; 205 bodies are
  their function's code under the impl's name; 19 functions have stack
  slots exchanged, same instructions and same frame — 3 of them already after
  the types alone, which moved no instruction. New symbols: the 6 impls of
  ADR 0168 and, for 42 value-receiver bodies, the pointer method the compiler
  generates for any method. A consumer importing every `pkg/v1` package, built
  `-trimpath`: the same 9 250 symbols; 16 SDK functions 1 to 5 bytes longer —
  one inline-mark `NOP` per inlined wrapper — and `go:func.*` (+496 bytes,
  the inlining tree's extra level).
- **benchstat**, eight interleaved runs at `-benchtime=100ms` of six packages
  holding wrappers, on an M1 Pro whose load average was 9 to 16: `errs`
  geomean −0.10 % (2 of 228 significant: `FieldInt` +1.7 % and `FieldInt64`
  +1.4 %, functions no wrapper reaches), `bson` −0.07 %, `i18n` −0.70 %,
  `trace` −0.79 %, `authz` +0.86 %, `websocket` +2.62 % (its loopback
  benchmarks vary ±31 % to ±73 %). With the machine code identical, these
  measure the machine.
- `docs/api` changes only in `file` fields.

## Consequences

- Of 7 208 exported declarations, 4 078 are written by kit (56.6 %), 849 of
  them declarations: kernel 27.6 % (11.1 % before), core 74.9 % (61.6 %),
  service 23.0 % (0 %), the framework 24.1 % (9.3 %), `pkg/v1` 90.6 %
  (89.8 %).
- A package's `CLAUDE.md` names its `decl_gen.go` and says that every body
  stays hand-written. Two notes that sat between declarations moved to their
  package's `CLAUDE.md` (`core/app/health`, `framework/internal/core/entitlement`);
  `core/net`'s `conn.go` and `packet.go` joined in `serve.go`
  (`KTN-STRUCT-PARTITION`), and that rule's exclusion of `core/data/sql` went
  with its cause.
- The exported surface is identical, so the release is a patch.

## What is left, and why: the next kit design step

The 3 130 declarations still written by hand, by cause:

1. **A wrapper is judged by inlinability, not by code.** 251 wrappers kit
   accepted changed the machine code. kit should compare code, not
   inlinability — an `-S` comparison of the functions a generation touches,
   instantiations and closures included — or write a single-`return` wrapper
   only, and measure the cost band against 20 as it does against 80.
2. **Re-exports outside `pkg/v1`** — `framework/model`, `framework/kit`,
   `framework/selfupdate` and `framework/entitlement` alias and forward
   their `internal/` halves (some 700 declarations), but only a layer whose
   shape is `facade` has a `facade:`, and the framework is one layer.
3. **Bodies too dear to wrap** — 513 functions and methods costing more than
   76: only a kit that writes the function whole could take them.
4. **Names** — 187 cheap enough whose lowered name is taken: predeclared
   (`String` → `string`, `Len`, `Error`, `Close`), a field (`Name()` over
   `name`), a package-scope name. kit accepts any unexported impl; the SDK
   needs a convention for the second spelling.
5. **Generics** — 306: generic functions, methods of generic types, the
   framework's generic methods (ADR 0168 §2).
6. **kit** — `-decls` refuses a package with no wrapper; `implements:` writes
   `T{}` for a method set that comes through an unexported pointer method;
   a wrapper whose impl shares a named result's name is written and does not
   compile; a type's doc directive does not survive `decl: true`.
7. **ktn-linter** — `KTN-STRUCT-COLOCATE` and `KTN-STRUCT-CTOR` read a
   declaration in `decl_gen.go` as absent, and the first honours no
   exclusion.
8. **kit's rules** — parenthesised and constrained declarations, sealed
   interfaces.

## References

- kitsunium/platform `feat/kit-gen-fixpoint` — kit `v0.1.0-rc.8`
- `make kit-coverage` (ADR 0169 §3), `make from-zero`
