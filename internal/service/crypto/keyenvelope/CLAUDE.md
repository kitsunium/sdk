# internal/service/crypto/keyenvelope

Password-wrapped DEK at rest — a pure composition over the registered
AES-256-GCM AEAD plus stdlib PBKDF2-SHA256.

## Frozen wire grammar (permanent post-v1.0.0)

```
$kenv$v=1$kdf=<id>$<b64salt>$aead=<id>$<b64box>
```

- `$`-delimited; `strings.Split("$")` yields exactly 7 elements (leading "").
- `<id>` for kdf = `pbkdf2-sha256` (the only id shipped; it pins 600000
  iterations — there is **no** iterations field). `<id>` for aead =
  `aes-256-gcm`. Both ids are extensible enums.
- `<b64salt>` / `<b64box>` use `base64.RawStdEncoding` (un-padded, PHC).

## KEK derivation (locked)

The KEK is computed with stdlib `crypto/pbkdf2` directly
(`pbkdf2.Key(sha256.New, passphrase, salt, 600000, KeyLen)`) — NOT the
PasswordHasher registry, whose internal salt would break the deterministic
KEK contract. This keeps the package dep-light (stdlib + core/crypto +
kernel/errs); x/crypto never leaks.

## AAD binding

The DEK is sealed with the envelope header bytes
`$kenv$v=1$kdf=pbkdf2-sha256$<b64salt>$aead=aes-256-gcm$` as AAD, so any
tamper of the framing fails the AEAD open.

## Why this shape

- **Mints no codes.** Structural faults → core `InvalidKeyEnvelope`
  (0.2.4.19); wrong passphrase → core `DecryptionFailed` forwarded verbatim
  (no key/password oracle beyond structural validity).
- **Zeroize.** The transient KEK is wiped via `defer` on every path. The
  caller-owned `dek` argument is never zeroized.
- **Self-activating.** Blank-imports `aesgcm` so the AEAD is registered.
