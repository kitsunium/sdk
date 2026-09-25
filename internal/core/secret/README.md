# secret (core)

The secret **domain** contract (ADR 0096): the `Value` a secret travels in, the
`Store` port that keeps named secrets as numbered versions, the `VersionValue`
a store hands back, and the one name grammar every store shares.

```go
var store coresecret.Store // built in internal/service/secret

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

Concrete stores, the keyring and the rotator: `internal/service/secret`. Public
facade: `pkg/v1/secret`. See `CLAUDE.md`.
