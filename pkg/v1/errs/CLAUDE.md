<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/errs/

## Purpose

Read-only public introspection of SDK errors. The concrete error type, constructors (`Define`, `Wrap`), and `FieldValue` helpers live in `internal/kernel/errs` and are intentionally NOT re-exported here. Consumers receive `error` values from the SDK and query them through the `Of`-family accessors. This keeps callers from forging SDK errors while still enabling dashboards, retries, and structured logging to branch on `Code` / `Reason` / `HTTPStatus` / `ExitCode`.

## Contents

```
accessors.go — type aliases (Code, Major, Layer, PkgCode, Serial, PrefixMatcher),
               mask constants (MaskByMajor|Layer|Package|Exact),
               and the var-grouped accessors block re-exporting
               CodeOf, CodeValueOf, ReasonOf, PublicOf, PrivateOf,
               LayerOf, HTTPStatusOf, ExitCodeOf, HasCode, HasReason,
               NewPrefixMatcher, Pack, ParseCode
```

`README.md` is the consumer-facing intro (Public/Private split, HTTP status policy, semantics reminders).

## Conventions

- **Pure aliases.** `type Code = kerrs.Code` (and the rest) — same Go type identity as the kernel value. Sharing a `Code` between SDK and consumer code is free at runtime and at the type checker.
- **Read-only.** No constructor is re-exported. `errs.Define` / `errs.Wrap` stay internal so consumers cannot mint codes outside the registry. Introspection happens through the `Of`-accessors, which all walk the `Unwrap() error` *and* `Unwrap() []error` chain and return the deepest `*errs.Error` value encountered.
- **No package code range.** `pkg/v1/errs` emits no errors of its own (no `codes.go`, no `errors.go`). It is a re-export layer; every code observable through it originated in some other package (origin wins on wrap — ADR 0005).
- **`CodeOf` is deprecated, kept at v1.** `CodeOf` returns `(int, bool)` for back-compat with the pre-ADR-0005 API. New code should use `CodeValueOf(err) (Code, bool)` for typed access. Same applies to `LayerOf` — prefer `CodeValueOf(err).Layer()`.
- **PrefixMatcher routes via `errors.Is`.** Use `NewPrefixMatcher(code, mask)` (combined with `MaskByMajor` / `MaskByLayer` / `MaskByPackage` / `MaskExact`) for CIDR-style code routing inside dashboards / middleware. Single-code matching uses `HasCode(err, c)` which is cheaper.
- **Defaults are global.** `HTTPStatusOf` returns 500 when no override exists; `ExitCodeOf` returns 70 (EX_SOFTWARE). Emitter packages set per-error overrides via `errs.WithHTTPStatus` / `WithExitCode` at `Define` time.

## Do NOT

- **Expose `PrivateOf(err)` output in any user-facing channel.** HTTP responses, gRPC responses, error pages, user-facing logs — never. `Private` is diagnostic-only. Use `PublicOf` for wire-safe messages (literal, ≤120 runes per kernel contract).
- Try to construct an `*errs.Error` from consumer code. The Go `internal/` rule blocks the import; the design is deliberate.
- Re-export `errs.Define`, `errs.Wrap`, or any constructor here. If a consumer needs a sentinel of their own, they declare their own error type — they don't mint SDK codes.
- Rely on specific default values beyond 500 / 70 — emitter packages may override per error, and a future ADR may broaden the defaults.

## Verification

```
bazel test --config=race //pkg/v1/errs:errs_test
# Fallback:
cd pkg/v1 && GOWORK=off go test -race ./errs/...
```

`accessors_external_test.go` walks every accessor against a real failure path (`logger.NewText(Config{})` → `WriterRequired`) and against stdlib-only / nil cases.
