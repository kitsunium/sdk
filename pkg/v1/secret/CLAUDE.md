# pkg/v1/secret/

## Purpose

Public facade for the secret domain (**ADR 0096**): aliases onto
`internal/core/secret` (`Value`, `Store`, `Versioned`, the port's sentinels)
and `internal/service/secret` (the three store configurations, `Keyring`,
`Policy`, `RotatorConfig`, `Rotator`, the engines' sentinels), plus thin
forwarding constructors. No logic lives here.

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
| `NotFound` … `KeyFileInvalid` | the fifteen sentinels of both layers |

## Why-this-shape

- `Policy` aliases `service/secret.PolicySpec` and the three configs alias the
  service layer, per ADR 0074: they are one engine's construction parameters,
  not something a second `Store` implementation would have to accept.
- The slog proof lives in `pkg/v1/logger/slogbridge`, the one package allowed to
  import `log/slog` (ADR 0032).

## Do NOT

- Hand-edit `README.md` — regenerate with `make docs-readme`.
- Add logic here; it belongs in `internal/service/secret`.

## Verification

```
bazel test --config=race //pkg/v1/secret:secret_test
```
