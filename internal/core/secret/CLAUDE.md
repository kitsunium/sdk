# internal/core/secret/

## Purpose

The SDK's secret **domain** contract (**ADR 0096**): the `Value` a secret
travels in, the `Store` port that keeps named secrets as numbered versions, the
`VersionValue` a store returns, and `ValidateName`, the one grammar every store
shares. Since **ADR 0142** it also holds the subject-key contract: the
`SubjectKeyStore` port that keeps one wrapped data key per subject, the
`SubjectKeyValue` it yields, and `ValidateSubject`, the reference grammar. The
concrete stores (memory, environment, file), the `Keyring`, the `Rotator`, the
`SubjectKeys` engine and the memory `SubjectKeyStore` live in
`internal/service/secret`.

Code range: `0.2.37.*` (ADR 0096; `0.2.37.8` added by ADR 0142).

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

**A subject is a lowercase reference, never an identity (ADR 0142).** It is the
KEY a caller's store files a wrapped data key under and a field of every box,
kept in clear in both. Lowercase only, because a store on a case-insensitive
collation (several SQL engines' default) would fold `Ab` and `aB` into one
subject, and two subjects sharing one data key erase each other. 1–128 bytes of
`a-z 0-9 - _ . :`, first byte a letter or digit — room for a SHA-512 in hex.
A refused subject is never echoed: the string most likely to be refused is the
e-mail address that should have been hashed.

**The subject-key port is the caller's, and two of its methods must be atomic.**
`SubjectKeyStore` is frozen at five (`Get`/`Insert`/`Replace`/`Delete`/`All`,
ADR 0039). Absent and taken are answers with a nil error, as in the
state-machine port (ADR 0120). `Insert` must be insert-if-absent and `Replace`
compare-and-swap across every process sharing the storage: otherwise two first
writers file two keys (the overwritten one's boxes open nowhere) and a re-wrap
racing an erasure writes the destroyed key back. `Delete` is the destruction a
cryptographic erase rests on; `All` holds no lock across a yield, because the
engine replaces keys while it ranges.

## Surface

| Symbol | Notes |
|---|---|
| `Value` | `NewValue` / `FromString`; `Reveal` / `RevealString` / `IsZero` / `Len` / `Equal`; `String` / `GoString` / `Format` / `MarshalJSON` / `MarshalText` / `UnmarshalJSON` / `UnmarshalText` |
| `Redacted` | the placeholder, `"<redacted>"` |
| `Store` | `Get` / `Versions` / `Put` / `Prune` / `Names` — **FROZEN at five** (ADR 0039), guarded by `TestStoreIsFrozenAtFiveMethods` |
| `VersionValue` | `Name`, `Version`, `Value`, `Created` — aliased as `pkg/v1/secret.Versioned` |
| `ValidateName`, `MaxNameLen` (63) | the DNS-label grammar |
| `SubjectKeyStore` | `Get` / `Insert` / `Replace` / `Delete` / `All` — **FROZEN at five** (ADR 0039, ADR 0142), guarded by `TestSubjectKeyStoreIsFrozenAtFiveMethods`; implemented by the caller, or `service/secret.NewMemorySubjectKeyStore` |
| `SubjectKeyValue` | `Subject`, `Wrapped` — what `All` yields; aliased as `pkg/v1/secret.WrappedKey` |
| `ValidateSubject`, `MaxSubjectLen` (128) | the subject grammar |
| `NotFound` `0.2.37.1` | no version under that name (EX_CONFIG) |
| `InvalidName` `0.2.37.2` | outside the grammar; the rejected string is never repeated |
| `ReadOnly` `0.2.37.3` | a write to a store that only reads |
| `StoreUnavailable` `0.2.37.4` | the backend failed — the one retryable verdict (EX_TEMPFAIL, 503) |
| `ValueRefused` `0.2.37.5` | a decode given a non-string token or the placeholder |
| `EmptyValue` `0.2.37.6` | `Put` of an empty secret |
| `InvalidKeep` `0.2.37.7` | `Prune` asked to keep fewer than one version |
| `InvalidSubject` `0.2.37.8` | outside the subject grammar; the rejected string is never repeated (ADR 0142) |

## Conventions

- Mixed receivers on `Value` are required by `encoding/json` and are the
  security property (every renderer on the VALUE's method set); exempted in
  `.ktn-linter.yaml` beside `core/net.DurationValue`.
- No Public, Private or field ever carries a secret; a VALID name may travel as
  a log-only field.

## Do NOT

- Add a rendering method that writes anything but `Redacted`.
- Add a sixth method to `Store` or to `SubjectKeyStore` — add a sibling interface.
- Admit an uppercase letter into the subject grammar.
- Import `log/slog` here (ADR 0032).

## Verification

```
bazel test --config=race //internal/core/secret:secret_test
```
