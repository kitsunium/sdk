# internal/service/secret/

## Purpose

The concrete half of the secret domain (**ADR 0096**): three `core/secret.Store`
implementations — memory, the process environment, a directory on disk — the
`Keyring` that sees one secret's versions as keys, and the `Rotator` that mints
new versions on a schedule. Everything reuses the SDK's own mechanisms: `crypto`
(AES-256-GCM, HKDF-SHA256, HMAC-SHA256), `vfs` (atomic publication), `lock`
(the file locker), `clock` (every stamp and every wait).

Code range: `0.3.68.*` (ADR 0096).

## Files

| File | Holds |
|---|---|
| `versions.go` | the argument checks every store shares (`checkPut`, `checkPrune`), `nextVersion`, `pruned` |
| `memory.go` | `NewMemory` / `MemoryConfig` — a map under an RWMutex |
| `env.go` | `NewEnv` / `EnvConfig` — read-only, the `NAME_FILE` convention |
| `file.go`, `file_config.go`, `file_record.go`, `fileguard_*.go` | `NewFile` / `FileConfig` — the directory store |
| `keyring.go` | `Keyring` / `NewKeyring` — seal, open, sign, verify over versions |
| `rotator.go` | `Rotator` / `NewRotator` / `RotatorConfig` / `PolicySpec` / `Random` |
| `keyfile.go` | `KeyFile` — the machine-local key for a sealed file store |
| `codes.go` / `errors.go` | `0.3.68.*` and `wrapAs` |

## Decisions worth not relitigating

- **Environment: both `NAME` and `NAME_FILE` set is `ENV_REFUSED`**, never a
  precedence rule — the official container images refuse it too. One trailing
  line ending (`\n` or `\r\n`) is removed from a file; an empty variable is
  unset; an empty or >64 KiB file is refused; an unreadable file is the
  retryable `StoreUnavailable` (a volume may not be mounted yet). A name whose
  variable ends in `_FILE` is refused by this store. No refusal repeats a value
  or a path — the VARIABLE names travel.
- **File store: one JSON document per secret**, the whole history, so a `Put`
  or a `Prune` is ONE `vfs.WriteAtomic`. The directory is created 0700 and an
  existing one with a group/world bit is REFUSED, never chmod-ed. Records are
  0600, named `<name>.secret`; the lock files (`<hex>.lock`, from the lock
  domain) and vfs temporaries (`.vfs-*.tmp`) share the directory and are not
  listed. With a key, the document is sealed (AES-256-GCM) with the associated
  data `"kitsunium/secret file record v1\x00" + name`, so a record copied under
  another name does not open; without one it is base64 in a 0600 file. A store
  opened with the wrong key, or with none, reads `RECORD_UNREADABLE` — and a
  `Put` never writes over a record it cannot read.
- **Writers take the lock domain's file locker, readers take nothing**: the
  rename is the synchronisation. A cancelled wait for the lock returns the
  context's own error; the release runs on `context.WithoutCancel`.
- **The platform gate is decided before the directory is created**
  (`fileguard_*.go`, the same tag set as `vfs`'s `osguard_*.go`).
- **Keyring box**: `0x01 | uint32 BE version | crypto box`; signature
  `0x01 | version | 32-byte HMAC`. The binding (AAD / signed prefix) is
  `"kitsunium/secret keyring v1\x00" + name + "\x00" + version`, then the
  caller's bytes. A version must be exactly one `crypto.Key` long; HKDF-SHA256
  with two labels makes its AEAD key and its MAC key two keys. `Open`/`Verify`
  collapse every box-shaped failure into one verdict and pass only
  `StoreUnavailable` through (`verdictOr`).
- **KeyFile**: exactly 32 RAW bytes — no encoding, no line ending — and
  anything else is `KEY_FILE_INVALID`, never truncated, padded or decoded. An
  absent file is written to a `.keyfile-*.tmp` beside it (0600, flushed) and
  published with `os.Link`, which fails on an existing name: the loser of a
  first-use race reads the winner's file, so concurrent callers agree on one
  key. The directory is made 0700 when absent and flushed after the link. An
  existing file with a group/world bit is `INVALID_CONFIG` (the store
  directory's rule). The same platform gate as the file store. Errors carry the
  operation, never the path, the key or a refused file's content.
- **Rotator**: every `PolicySpec` field required (`Keep` ≥ 2); `Random(n)`
  refuses n < 16 at call time; a rotation whose prune failed returns the new
  version AND the error; `OnRotate` runs after every lock is released; `Run`
  blocks on the caller's goroutine, returns nil on cancellation and the first
  error otherwise, and waits one whole interval rather than spinning when the
  store's clock and its own disagree. The optional `Locker` serialises across
  processes; the mutex serialises within one.

## Surface

| Symbol | Notes |
|---|---|
| `NewMemory(MemoryConfig) Store` | cannot fail |
| `NewEnv(EnvConfig) (Store, error)` | refuses a prefix no shell exports |
| `NewFile(FileConfig) (Store, error)` | also `io.Closer`; `UnsupportedPlatform` off Unix |
| `NewKeyring(Store, name) (*Keyring, error)` | `Seal` / `Open` / `Sign` / `Verify` |
| `NewRotator(RotatorConfig) (*Rotator, error)` | `Ensure` / `Due` / `RotateIfDue` / `Rotate` / `Run` |
| `PolicySpec`, `Random(n)` | the policy and its generator |
| `InvalidConfig` `0.3.68.1` | a constructor's refusal, naming the setting |
| `RecordUnreadable` `0.3.68.2` | a file record that exists and does not read back |
| `EnvRefused` `0.3.68.3` | the environment named a secret without a usable value |
| `SealInvalid` `0.3.68.4` | `Keyring.Open`, every box-shaped failure |
| `SignatureInvalid` `0.3.68.5` | `Keyring.Verify`, every signature-shaped failure |
| `KeyMaterialInvalid` `0.3.68.6` | a keyring version is not one `crypto.Key` long |
| `GenerateFailed` `0.3.68.7` | the policy's generator failed or returned nothing |
| `KeyFileInvalid` `0.3.68.8` | a key file that is not a regular file of exactly 32 raw bytes |
| `KeyFile(path) (crypto.Key, error)` | the machine-local key |

## Tests

`store_external_test.go` runs one conformance table against memory, file and
sealed file (the file stores join wherever a probe `NewFile` does not answer
`UnsupportedPlatform`). The environment tests call `t.Setenv` and therefore do
not run in parallel.

## Do NOT

- Wrap a decoder's error message into a field: it can quote the record.
- Add a precedence rule between `NAME` and `NAME_FILE`.
- Start a goroutine from the rotator.

## Verification

```
bazel test --config=race //internal/service/secret:secret_test
```
