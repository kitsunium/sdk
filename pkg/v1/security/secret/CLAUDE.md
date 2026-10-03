# pkg/v1/security/secret/

## Purpose

Public facade for the secret domain (**ADR 0096**): aliases onto
`internal/core/security/secret` (`Value`, `Store`, `Versioned`, and every
sentinel — the port's and the engines', all declared there since ADR 0160)
and `internal/service/security/secret` (the three store configurations,
`Keyring`, `Policy`, `RotatorConfig`, `Rotator`), plus thin
forwarding constructors — and, since **ADR 0142**, the subject keys in
`subjectkeys.go`: `SubjectKeys` over a `SubjectKeyStore`. No logic lives here.

## Surface

| Symbol | Role |
|---|---|
| `Value`, `New`, `FromString`, `Redacted` | the secret, and the placeholder every rendering writes |
| `Store`, `Versioned` | the frozen five-method port and one version |
| `ValidateName`, `MaxNameLen` | the name grammar |
| `NewMemory` / `MemoryConfig` | in-process store |
| `NewEnv` / `EnvConfig` | read-only environment store, `NAME_FILE` convention |
| `NewFile` / `FileConfig` | directory store, optional sealing, `io.Closer` |
| `KeyFile(path) (crypto.Key, error)` | the machine-local key for `FileConfig.Key`: 32 raw bytes, created on first use (0600, directory 0700) and published by hard link so concurrent first uses agree on one key |
| `Keyring`, `NewKeyring` | versions as keys |
| `Policy`, `Random`, `Rotator`, `RotatorConfig`, `NewRotator` | rotation |
| `SubjectKeys`, `SubjectKeysConfig`, `NewSubjectKeys` | one data key per subject under a rotating root: `Seal` / `Open` / `Destroy` / `Rewrap` / `OldestRoot` (ADR 0142) |
| `SubjectKeyStore`, `WrappedKey`, `NewMemorySubjectKeyStore` | the frozen five-method port a caller implements (atomic `Insert` and `Replace`), what it yields, and the memory one |
| `RewrapReport`, `SubjectOf`, `ValidateSubject`, `MaxSubjectLen` | a pass's report, a box's subject, the reference grammar (lowercase, 1–128 bytes) |
| `NotFound` … `SubjectKeyUnreadable` | the eighteen sentinels — the port's eight and the engines' ten, all aliasing `internal/core/security/secret` (ADR 0160) (`InvalidSubject`, `KeyDestroyed`, `SubjectKeyUnreadable` added by ADR 0142) |

## Why-this-shape

- `Policy` aliases `service/security/secret.PolicySpec` and the three configs alias the
  service layer, per ADR 0074: they are one engine's construction parameters,
  not something a second `Store` implementation would have to accept. The
  same split holds for the subject keys: `SubjectKeyStore` and `WrappedKey`
  are the port's and alias core; `SubjectKeysConfig` and `RewrapReport` are
  the engine's and alias service.
- The subject keys live in their own file, `subjectkeys.go`, because
  ktn-linter's cohesion rule reads the facade as one file per seam.
- The slog proof lives in `pkg/v1/observe/logger/slogbridge`, the one package allowed to
  import `log/slog` (ADR 0032).

## Do NOT

- Hand-edit `README.md` — regenerate with `make docs-readme`.
- Add logic here; it belongs in `internal/service/security/secret`.

## Verification

```
bazel test --config=race //pkg/v1/security/secret:secret_test
```
