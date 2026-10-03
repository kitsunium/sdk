# secret (core)

The secret **domain** contract (ADR 0096): the `Value` a secret travels in, the
`Store` port that keeps named secrets as numbered versions, the `VersionValue`
a store hands back, and the one name grammar every store shares.

```go
var store coresecret.Store // built in internal/service/security/secret

current, err := store.Get(ctx, "smtp-url")   // the newest version
fmt.Printf("%+v\n", current)                  // {Name:smtp-url Version:3 Value:<redacted> Created:…}
dsn := current.Value.RevealString()           // the only way the bytes come out
```

`Value` writes `<redacted>` in every rendering — `String`, `GoString`, `Format`
(so every fmt verb), `MarshalJSON`, `MarshalText` — decodes from a JSON string
or from text, refuses a non-string token and the placeholder itself, compares
with `Equal` in constant time, and does not compile with `==`.

`Store` is frozen at five methods (ADR 0039). Versions number from 1 and are
never reused. A name is 1–63 characters of `a-z`, `0-9` and `-`.

`SubjectKeyStore` (ADR 0142) keeps one wrapped data key per subject for the
subject-key engine, and is frozen at five methods too. The caller implements it
over its own storage; `Insert` must be insert-if-absent and `Replace`
compare-and-swap across every process, and absence is an answer, not an error:

```go
var keys coresecret.SubjectKeyStore // yours, or service/security/secret.NewMemorySubjectKeyStore()

inserted, err := keys.Insert(ctx, "9f86d081884c7d65", wrapped) // false: a key is filed already
replaced, err := keys.Replace(ctx, "9f86d081884c7d65", old, next) // false: destroyed or changed meanwhile
```

A subject is a reference, never an identity: 1–128 bytes of lowercase `a-z`,
`0-9`, `-`, `_`, `.`, `:` (`ValidateSubject`) — an HMAC of the identity, in hex.

Concrete stores, the keyring, the rotator and the subject keys:
`internal/service/security/secret`. Public facade: `pkg/v1/security/secret`. See `CLAUDE.md`.
