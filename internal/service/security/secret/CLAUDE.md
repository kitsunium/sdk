<!-- updated: 2026-10-03T07:14:15Z -->
# internal/service/security/secret/

> Reference moved to `internal/service/security/secret/NOTES.md` (ADR 0253), under the same section titles: Decisions worth not relitigating.

## Purpose

The concrete half of the secret domain (**ADR 0096**): three `core/security/secret.Store`
implementations — memory, the process environment, a directory on disk — the
`Keyring` that sees one secret's versions as keys, and the `Rotator` that mints
new versions on a schedule. Since **ADR 0142**, `SubjectKeys`: one data key per
subject, wrapped by a `Keyring` root that rotates, destroyed to erase what it
sealed, with the memory `core/security/secret.SubjectKeyStore`. Everything reuses the
SDK's own mechanisms: `crypto` (AES-256-GCM, HKDF-SHA256, HMAC-SHA256), `vfs`
(atomic publication), `lock` (the file locker), `clock` (every stamp and every
wait), `kernel/collections/cache` (the opened subject keys).

Codes: none declared here. The engines' refusals — `0.3.68.*`, allocated here
(ADR 0096; `0.3.68.9`–`0.3.68.10` added by ADR 0142) — and the port's
verdicts (`0.2.37.*`) are both declared in `internal/core/security/secret`
(ADR 0160) and only raised here; the `0.3.68.*` values were kept when their
declarations moved.

## Files

| File | Holds |
|---|---|
| `versions.go` | the argument checks every store shares (`checkPut`, `checkPrune`, `checkPruneBelow`), `nextVersion`, `pruned`, `prunedBelow` (the `FloorPruner` form every store here implements) |
| `memory.go` | `NewMemory` / `MemoryConfig` — a map under an RWMutex |
| `env.go` | `NewEnv` / `EnvConfig` — read-only, the `NAME_FILE` convention |
| `file.go`, `file_config.go`, `file_record.go`, `fileguard_*.go` | `NewFile` / `FileConfig` — the directory store |
| `keyring.go` | `Keyring` / `NewKeyring` — seal, open, sign, verify over versions |
| `keyring_view.go` | `rootView` — the keyring read ONCE, every usable version's sealing key derived once: what `Rewrap` seals and opens with |
| `subjectkeys.go` | `SubjectKeys` / `NewSubjectKeys` / `SubjectKeysConfig` — `Seal`, `Open`, `Destroy`, `Expire`, and the acquire / load / create / unwrap path |
| `subjectbox.go` | the subject box layout, `SubjectOf`, the length-prefixed binding, the wrap's associated data |
| `subjectcache.go` | `openedKey` (copies out, wipes in place) and `keyCache` (the `kernel/collections/cache` primitive plus the destruction epoch; `expire`, the caller's sweep) |
| `subjectrewrap.go` | `Rewrap` / `RewrapValue` and `OldestRoot` |
| `subjectmemory.go` | `NewMemorySubjectKeyStore` — the reference `core/security/secret.SubjectKeyStore` |
| `rotator.go` | `Rotator` / `NewRotator` / `RotatorConfig` / `PolicySpec` / `Random` |
| `keyfile.go` | `KeyFile` — the machine-local key for a sealed file store |
| `wrap.go` | `wrapAs` (a core sentinel as the origin, a cause as a field), and `storeFailure` (a `SubjectKeyStore` error as `StoreUnavailable`, the store's own error kept) |

## The store directory's rule, beside the other four

`app/lock`, `proc/ipc`, `security/session` and `data/queue` each refuse a
directory by a rule of their own, compared side by side in
`internal/kernel/fs/CLAUDE.md` §Five directory rules; they share the kernel's
measurements and lock, and no rule. This store's, and why it is not a
neighbour's:

- **Any group or other bit is refused** (`0o077`), READ included: a directory
  another account can list hands it the secret NAMES, which are the file
  names — so a group share `lock` and `queue` accept, and the group search
  `ipc` admits, are refused here. `session` is the one neighbour as strict, for
  the same reason.
- **Refused, never narrowed** — `session` narrows a directory it created; this
  store refuses even its own, so an umask or a default ACL that widened
  `MkdirAll`'s request is a refusal the operator sees, not a repair.
- **Judged twice, the second time on the HELD root** (`checkHeldRoot`), where
  `os.SameFile` must still match the path — as `session` does; `lock` and
  `queue` judge the path once.
- **The path above is not walked here.** It is audited by the lock domain's
  `checkChain` when `NewFile` opens its file locker over the same directory —
  AFTER `prepareDir`'s `MkdirAll`, so a path the audit refuses may already
  hold the new, empty `0700` directory inside the planter's tree, where `lock`
  and `session` audit BEFORE anything is created. Nothing is written there and
  `NewFile` fails, but the order is the one ADR 0083 warns against; moving the
  audit ahead of `prepareDir` is a behaviour change left to its own decision,
  named here rather than implied closed.
- **No Windows rule**: the store is refused there (`vfs` refuses the platform:
  no directory flush, a mode that is not an ACL), so neither `winacl` nor a
  mode is asked.

## Surface

| Symbol | Notes |
|---|---|
| `NewMemory(MemoryConfig) Store` | cannot fail |
| `NewEnv(EnvConfig) (Store, error)` | refuses a prefix no shell exports |
| `NewFile(FileConfig) (Store, error)` | also `io.Closer`; `UnsupportedPlatform` off Unix |
| `NewKeyring(Store, name) (*Keyring, error)` | `Seal` / `Open` / `Sign` / `Verify` |
| `NewRotator(RotatorConfig) (*Rotator, error)` | `Ensure` / `Due` / `RotateIfDue` / `Rotate` / `Run` |
| `PolicySpec`, `Random(n)` | the policy and its generator |
| `InvalidConfig` … `KeyFileInvalid`, `KeyDestroyed`, `SubjectKeyUnreadable` (`0.3.68.1`–`0.3.68.10`) | the engines' refusals: declared in `core/security/secret` beside the port's verdicts (ADR 0160) — its Surface table says what each one means — and raised here, bare or through `wrapAs` with log-only fields |
| `KeyFile(path) (crypto.Key, error)` | the machine-local key |
| `NewSubjectKeys(SubjectKeysConfig) (*SubjectKeys, error)` | `Seal(ctx, subject, plaintext, bind...)` / `Open(ctx, box, bind...)` / `Destroy` / `Expire() int` / `Rewrap` / `OldestRoot` |
| `SubjectKeysConfig` | `Root`, `Former`, `Store`, `CacheSize`, `CacheTTL`, `Clock` |
| `RewrapValue` | `Root`, `Current`, `Rewrapped`, `FromFormer`, `Skipped`, `Unreadable` |
| `SubjectOf(box) (string, error)` | the subject a box names, without opening it |
| `NewMemorySubjectKeyStore() core/security/secret.SubjectKeyStore` | the reference store; cannot fail |
| `RotatorConfig.InUse` | `func(ctx) (oldest int, err error)` — wire `SubjectKeys.OldestRoot` |

## Tests

`store_external_test.go` runs one conformance table against memory, file and
sealed file (the file stores join wherever a probe `NewFile` does not answer
`UnsupportedPlatform`). The environment tests call `t.Setenv` and therefore do
not run in parallel. The subject keys are covered by `subjectkeys_`,
`subjectrewrap_`, `subjectformer_`, `subjectfaults_` and `subjectmemory_external_test.go` (two
engines over one store stand for two processes; a scripted store injects
failures and an erasure in the middle of a re-wrap; a root renamed opens
what its former name wrapped only through `Former`, and a move from it
stopped half-way resumes), by
`subjectcache_internal_test.go` (every way a key leaves the cache wipes it) and
by `subjectkeys_internal_test.go` (a value wrapped under the root that is not
one key long is unreadable: never used as a key, never moved, pinning no
version).
`subjectkeys_bench_test.go` feeds `BENCH.md`; its
`BenchmarkSubjectKeysOldestRootPopulation`, added after `BENCH.md` was last
generated, has its figures under "Decisions worth not relitigating" until the
next regeneration.

## Do NOT

- Wrap a decoder's error message into a field: it can quote the record.
- Add a precedence rule between `NAME` and `NAME_FILE`.
- Start a goroutine from the rotator.
- Zero a cached subject key without its mutex, or hand a caller the cached
  bytes themselves: a call would seal under a key an eviction cleared.
- Read an absent subject key, or one under another identifier, as anything but
  `KEY_DESTROYED` — and never read `SUBJECT_KEY_UNREADABLE` as an erasure.
- Replace a subject key the engine could not unwrap.
- Declare an error code here — an `errs.Define` or a `Code*` constant. Every
  code of the domain is declared in `internal/core/security/secret`, at this
  package's path in the core (ADR 0160); a new refusal gets its code and
  sentinel there, in the range this package already owns (`0.3.68.*`), and is
  raised here.

## Verification

```
bazel test --config=race //internal/service/security/secret:secret_test
```
