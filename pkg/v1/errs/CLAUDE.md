<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/errs/

## Purpose

Public **introspection and construction** of SDK errors (ADR 0019). The concrete `*errs.Error` type stays internal — consumers see `error`, never the struct, so they cannot forge one by literal. But they CAN mint typed errors in the SDK model via `New` / `Wrap` (+ `Field` helpers), and inspect any SDK error via the `Of`-family accessors. This lets a downstream codebase adopt the model wholesale (every error a dotted-quad `Code` + wire-safe `Public` + log-only `Private`) instead of only observing SDK-origin errors, while dashboards, retries, and structured logging branch on `Code` / `Reason` / `HTTPStatus` / `ExitCode` regardless of who built the error.

## Contents

```
doc.go       — the package comment (both halves: introspection and construction);
               kit writes it from `design/kernel/errs.yaml` (ADR 0167)
accessors.go — type aliases (Code, Major, Layer, PkgCode, Serial,
               PrefixMatcher), mask constants (MaskByMajor|Layer|Package|Exact),
               the forwarders CodeOf, PublicOf, PrivateOf, HTTPStatusOf,
               ExitCodeOf, FieldsOf, HasCode, HasReason, NewPrefixMatcher,
               Pack, ParseCode; ReasonOf, still a variable (Conventions);
               HasAnyCode/HasAnyReason
construct.go — construction surface: New, Wrap (+ WrapParams alias), the Field
               alias + the String/Int/Int64/Bool/Float/NewFieldValue forwarders,
               and the MinAppMajor/MaxMajor application-code-range constants
accessors_external_test.go / construct_external_test.go — the facade suites
               (see Verification)
accessors_bench_test.go — the benchmarks BENCH.md's first table comes from
facade_bench_test.go — one call to each forwarded name: BENCH.md's
               variable-against-forwarder table
BENCH.md     — generated benchmark report
USES.md      — the interactive use-case tabs, hand-authored because HTML in a Go
               doc comment renders as literal text
```

`README.md` is the consumer-facing intro (construction, Public/Private split, HTTP status policy, code-space rules, semantics reminders) — written by `tools/genindex` from the committed `docs/api` (`make docs-readme`, ADR 0167).

## Conventions

- **Pure aliases.** `type Code = kerrs.Code` / `type Field = kerrs.FieldValue` / `type WrapParams = kerrs.WrapParams` — same Go type identity as the kernel value. Sharing a `Code` / `Field` between SDK and consumer code is free at runtime and at the type checker.
- **A function is forwarded, never held in a variable — but one.** Every function here is a `func` calling the kernel's, spelled with this package's aliases: `func HasCode(err error, code Code) bool { return kerrs.HasCode(err, code) }`. Until the forwarders change the eighteen were `var HasCode = kerrs.HasCode` and its siblings — a published shape changed while v0, under ADR 0040; seventeen changed, `ReasonOf` did not. A variable can be reassigned for the whole process by any consumer, and a call through it is indirect: the inliner, escape analysis and constant folding cannot see the callee, so `Pack(0x40, 1, 1, 1)` could not fold to a constant. Each name was decided on its own by one rule: a forwarder ships when `go build -gcflags=-m` reports `can inline` (sixteen: `PublicOf`/`PrivateOf` cost 77, `HTTPStatusOf`/`ExitCodeOf`/`FieldsOf` 61, `HasCode`/`HasReason` 62, `ParseCode` 70, `Pack` 25, `NewPrefixMatcher` 12, `NewFieldValue` 18, the other Field builders 13, against the budget of 80), or when benchstat over `-count=10` shows no regression beyond 3 % at p < 0.05; a name failing both stays a variable, listed with its numbers for ADR 0163. `CodeOf` and `ReasonOf` cannot inline (cost 88: each inlines the kernel accessor, 75 on its own), and the accessor's two returns merge into the forwarder's one, adding a taken branch and a `CMP`/`CSET` on every call. Over four runs of ten samples, CPU time per call (BENCH.md): `CodeOf` +2.62 %, +1.15 %, +2.03 %, +4.11 %, never significant at 0.05 — it ships; `ReasonOf` +1.88 %, +1.07 %, +3.88 % (p = 0.035), +5.13 % (p = 0.043) — beyond the rule in two runs, so **`ReasonOf` stays `var ReasonOf = kerrs.ReasonOf`**, the one function variable left in `pkg/v1`, for ADR 0163 with those numbers (pooled over the forty samples: +2.91 %, p < 0.001; `CodeOf` +2.77 %, p = 0.001). Both would inline if the kernel accessors returned from one place — a kernel change, with its own measurement. Since ADR 0166 the verdict is kit's rather than an exception: `design/sdk.yaml` gives `ReasonOf` an inline budget, kit measures its forwarder (`-gcflags=-m=2`, linux/amd64) and writes the variable while it does not inline, recording the compiler's words in `facade_gen.go`; once it inlines, `kit gen` writes the forwarder. Never assign to `ReasonOf`: it would change it for every caller in the process.
- **Construction is runtime-validated, never panicking.** `New` and `Wrap` delegate to the kernel's non-panicking path (`NewRuntime` / `Wrap`): a structural failure (bad code, non-`SCREAMING_SNAKE` reason, empty/>120-rune/multiline public, empty private) returns the specific typed `CodeInvalid*` error rather than aborting. The internal `errs.Define` (panic-at-init, AST-audited for SDK-internal sentinels) is NOT re-exported — external callers get no AST audit, so they take the runtime-validated entry points.
- **Third-party code space.** Consumers assign codes a Major in `[MinAppMajor, MaxMajor]` (`0x40–0x7F`); the SDK only ever allocates Major `< 0x40` (0 internal, 1+ semver). `MaxMajor` is `0x7F` because every `Code` must round-trip through a positive int32. Declare codes as hex literals (`0x40_LL_PP_SS`) exactly as the SDK does internally. See ADR 0019.
- **Introspection walks the chain.** The `Of`-accessors all walk the `Unwrap() error` *and* `Unwrap() []error` chain and return the deepest `*errs.Error` value encountered. Origin wins on `Wrap` — the observed `Code` is the cause's, never the wrap site's.
- **No package code range.** `pkg/v1/errs` emits no errors of its own (no `codes.go`, no `errors.go`). It is a re-export layer; every code observable through it originated in some other package (origin wins on wrap — ADR 0005).
- **Single typed accessor per concept.** `CodeOf(err) (Code, bool)` returns the typed `Code`; the layer octet is composable via `code.Layer()`. There is no int-returning shim and no parallel `LayerOf` — the kernel exports exactly one accessor per field and `pkg/v1/errs` forwards it unchanged.
- **PrefixMatcher routes via `errors.Is`.** Use `NewPrefixMatcher(code, mask)` (combined with `MaskByMajor` / `MaskByLayer` / `MaskByPackage` / `MaskExact`) for CIDR-style code routing inside dashboards / middleware. Single-code matching uses `HasCode(err, c)` which is cheaper.
- **Fields are read through `FieldsOf` and the `Field` alias's own methods.** `FieldsOf(err)` forwards to the kernel accessor unchanged — every field on the chain, oldest cause first, newest wrapper last, a copy, `nil` without an SDK error — and a `Field` answers `Key()` and `StringValue()`. There is no map-shaped or by-key helper on purpose: a key can appear at several depths of one chain, and the caller is the one who knows which depth it wants.
- **A field value is a clause the emitter chose, never a secret it was given.** Names, positions, reasons: `mail.ParseURL` names the part of the URL that is wrong and never the URL; the secret domain names a secret, never its value. That is what makes `FieldsOf` safe to expose, and the accessor's doc says so. A `cause` field is the exception to "chosen" — a foreign error's text, as its library wrote it.
- **Defaults are global.** `HTTPStatusOf` returns 500 when no override exists; `ExitCodeOf` returns 70 (EX_SOFTWARE). Emitter packages set per-error overrides via `errs.WithHTTPStatus` / `WithExitCode` at `Define` time.

## Do NOT

- **Expose `PrivateOf(err)` output in any user-facing channel.** HTTP responses, gRPC responses, error pages, user-facing logs — never. `Private` is diagnostic-only. Use `PublicOf` for wire-safe messages (literal, ≤120 runes per kernel contract).
- Construct an `*errs.Error` by struct literal or expose the concrete type — `New` / `Wrap` return `error`, and the struct stays unexported so it cannot be forged.
- Re-export a function through a variable (`var F = kerrs.F`) — the design declares a forwarder and kit writes it (Conventions); `ReasonOf` is a variable only because its inline budget fails, measured by kit (ADR 0166), and a second needs the same budget and the same benchmark. A value — a mask constant, a type — is still re-exported as one.
- Re-export the internal `errs.Define` here — it panics at init and is meant for AST-audited SDK-internal sentinels. The public construction path is the runtime-validated `New` / `Wrap` (ADR 0019); a consumer minting its own sentinel uses those with an application-range code (Major `0x40–0x7F`).
- Let a consumer assign codes a Major `< MinAppMajor` (`0x40`) — that range belongs to the SDK and a future SDK release may collide with it.
- **Put `FieldsOf` output on the wire.** Safe to read is not safe to publish: fields are diagnostics like `Private` — authz attaches the subject, the action and the resource — and they go to logs and operator reports only.
- Parse `StringValue()` back into a type — it is a rendering for reading, and the kernel promises nothing more.
- Rely on specific default values beyond 500 / 70 — emitter packages may override per error, and a future ADR may broaden the defaults.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/kernel/errs.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

`ReasonOf` is the one forwarder written as a variable bound to its callee: `design/sdk.yaml` gives it an inline budget, the compiler measures its forwarder at 88 against the inliner's 80, and `facade_gen.go` records the verdict above it. Once the kernel accessor inlines within the budget, `kit gen` writes it as a function — no design edit. Every other forwarder is a function, whatever it costs.

## Verification

```
bazel test --config=race //pkg/v1/errs:errs_test
# Fallback:
cd pkg/v1 && GOWORK=off go test -race ./errs/...
```

`cd pkg && GOWORK=off go build -gcflags=-m ./v1/errs` reports `can inline` for every forwarder but `CodeOf` (cost 88; `ReasonOf` is the variable), and `cd pkg && GOWORK=off go test -run='^$' -bench=Facade -count=10 ./v1/errs` prices each forwarded name (`facade_bench_test.go`).

`accessors_external_test.go` walks every accessor against a real failure path (`logger.NewText(Config{})` → `WriterRequired`) and against stdlib-only / nil cases, and reads `mail.ParseURL`'s `problem` clause through `FieldsOf` — in order under a consumer's own wrap, with the URL's password in no field and no rendering.

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): `Wrap`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name.
