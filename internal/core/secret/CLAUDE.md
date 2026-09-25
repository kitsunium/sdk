# internal/core/secret/

## Purpose

The SDK's secret **domain** contract (**ADR 0096**): the `Value` a secret
travels in, the `Store` port that keeps named secrets as numbered versions, the
`VersionValue` a store returns, and `ValidateName`, the one grammar every store
shares. The concrete stores (memory, environment, file), the `Keyring` and the
`Rotator` live in `internal/service/secret`.

Code range: `0.2.37.*` (ADR 0096).

## Why this shape

**A secret must not be written down by accident.** `Value` is the mechanism, not
a convention: `String`, `GoString`, `Format`, `MarshalJSON` and `MarshalText`
all write `Redacted` (`<redacted>`), whatever is held — the empty secret too,
because a rendering that varied would say whether a secret is set. `Format` is
the one people forget: without it `%x` and `%d` format the struct. `Reveal` (a
copy) and `RevealString` are the only exits.

**The bytes sit behind a pointer.** fmt cannot call methods on a value it
reaches through an UNEXPORTED field of another struct; it prints that field's
fields by reflection. A `[]byte` there would print as a list of byte values; a
pointer prints an address. `TestValueNeverRendersItsSecret` asserts both cases.

**`==` does not compile.** A zero-size `[0]func()` field makes the struct
non-comparable, so neither a byte comparison (leaks a shared prefix's length)
nor a pointer comparison (answers "same allocation") can be spelled; `Equal` is
`crypto/subtle`. `TestValueIsNotComparable` pins it.

**Decoding accepts a string and nothing else.** A JSON number has already been
re-spelled before `UnmarshalJSON` sees it (`1e3` → `1000`, twenty digits →
`float64`), so it is refused; so is `Redacted` itself, because finding it on the
way in means a rendered configuration was loaded as a real one. `null` leaves
the field untouched.

**No `LogValue`.** `slog.LogValuer` would put `log/slog` in core, which ADR 0032
forbids. slog's handlers reach for `TextMarshaler`/`json.Marshaler`/fmt, all of
which redact — asserted in `pkg/v1/logger/slogbridge`.

**Versions, never reused.** A version number is how a sealed box names its key
(the keyring), so a reused number would hand an old box to a new key.

**A closed name alphabet.** A name is a file name, must survive case-insensitive
filesystems, and maps one-to-one onto an environment variable only because `_`
is not in it. An invalid name is never echoed in a refusal.

## Surface

| Symbol | Notes |
|---|---|
| `Value` | `NewValue` / `FromString`; `Reveal` / `RevealString` / `IsZero` / `Len` / `Equal`; `String` / `GoString` / `Format` / `MarshalJSON` / `MarshalText` / `UnmarshalJSON` / `UnmarshalText` |
| `Redacted` | the placeholder, `"<redacted>"` |
| `Store` | `Get` / `Versions` / `Put` / `Prune` / `Names` — **FROZEN at five** (ADR 0039), guarded by `TestStoreIsFrozenAtFiveMethods` |
| `VersionValue` | `Name`, `Version`, `Value`, `Created` — aliased as `pkg/v1/secret.Versioned` |
| `ValidateName`, `MaxNameLen` (63) | the DNS-label grammar |
| `NotFound` `0.2.37.1` | no version under that name (EX_CONFIG) |
| `InvalidName` `0.2.37.2` | outside the grammar; the rejected string is never repeated |
| `ReadOnly` `0.2.37.3` | a write to a store that only reads |
| `StoreUnavailable` `0.2.37.4` | the backend failed — the one retryable verdict (EX_TEMPFAIL, 503) |
| `ValueRefused` `0.2.37.5` | a decode given a non-string token or the placeholder |
| `EmptyValue` `0.2.37.6` | `Put` of an empty secret |
| `InvalidKeep` `0.2.37.7` | `Prune` asked to keep fewer than one version |

## Conventions

- Mixed receivers on `Value` are required by `encoding/json` and are the
  security property (every renderer on the VALUE's method set); exempted in
  `.ktn-linter.yaml` beside `core/net.DurationValue`.
- No Public, Private or field ever carries a secret; a VALID name may travel as
  a log-only field.

## Do NOT

- Add a rendering method that writes anything but `Redacted`.
- Add a sixth method to `Store` — add a sibling interface.
- Import `log/slog` here (ADR 0032).

## Verification

```
bazel test --config=race //internal/core/secret:secret_test
```
