# pkg/v1/crypto/

## Purpose

Stable v1 public facade for authenticated encryption: `Seal` / `Open` with a
hidden nonce. Thin alias + validate-and-delegate layer over
`internal/core/crypto` (ADR 0013). Importing the package activates the default
**AES-256-GCM** scheme (stdlib-only), so `go get`-ing this package pulls **zero**
non-stdlib dependencies — the dep-light invariant.

Consumer-facing prose lives in the package doc comment (`crypto.go`) and is
rendered to `README.md` by gomarkdoc — edit the doc comment, not the README.

## Contents

```
crypto.go  — Key / Algorithm aliases, KeyLen + AESGCM consts, NewKey,
             Seal / SealAs / Open; blank-imports the aesgcm activator
README.md  — generated from the package doc comment (make docs-readme)
```

## Conventions

- **Aliases, not new types.** `Key` / `Algorithm` are `= internal/core/crypto`
  aliases — identity-equal to the internal model, zero runtime cost.
- **Thin wrappers.** `NewKey` / `Seal` / `SealAs` / `Open` nil-validate-free
  delegate to the core dispatcher; no crypto logic lives here.
- **Default scheme via blank import.** `crypto.go` blank-imports
  `internal/service/crypto/aesgcm`; that is the only thing wiring the default
  algorithm, and it is stdlib-only. Future schemes (XChaCha20-Poly1305) activate
  via their own blank import — never auto-pulled here, to preserve dep-light.
- **The nonce is never in the API.** `Seal` generates and embeds it; `Open`
  strips it. There is no nonce parameter.
- **`Open` is non-oracle.** Every failure returns the same `DecryptionFailed`.

## Dep-light invariant

```sh
# Must list zero golang.org/x/crypto (or any non-stdlib crypto) modules:
cd pkg/v1 && GOWORK=off go list -deps ./crypto/... | grep -i 'x/crypto' && echo LEAK || echo "dep-light OK"
```

## Do NOT

- Re-export `errs.Define` / construct `*errs.Error` here — introspect via
  `pkg/v1/errs` accessors.
- Auto-import a vendor-dependent scheme (argon2, XChaCha20) from this package —
  that would break the dep-light guarantee. Each lives behind its own activator.
- Log `Key.Bytes()` or surface it in an error message.

## Verification

```sh
bazel test --config=race //pkg/v1/crypto:crypto_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./crypto/...
```
