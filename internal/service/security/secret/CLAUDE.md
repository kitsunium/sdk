<!-- updated: 2026-10-03T07:14:15Z -->
# internal/service/security/secret/

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
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `EnvConfig`, `FileConfig`, `Keyring`, `MemoryConfig`, `PolicySpec`, `RotatorConfig`, `Rotator`, `SubjectKeysConfig`, `SubjectKeys` and `RewrapValue` — each struct with every field, unexported ones included; `NewMemorySubjectKeyStore`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| `versions.go` | the argument checks every store shares (`checkPut`, `checkPrune`), `nextVersion`, `pruned` |
| `memory.go` | `NewMemory` / `MemoryConfig` — a map under an RWMutex |
| `env.go` | `NewEnv` / `EnvConfig` — read-only, the `NAME_FILE` convention |
| `file.go`, `file_config.go`, `file_record.go`, `fileguard_*.go` | `NewFile` / `FileConfig` — the directory store |
| `keyring.go` | `Keyring` / `NewKeyring` — seal, open, sign, verify over versions |
| `keyring_view.go` | `rootView` — the keyring read ONCE, every usable version's sealing key derived once: what `Rewrap` seals and opens with |
| `subjectkeys.go` | `SubjectKeys` / `NewSubjectKeys` / `SubjectKeysConfig` — `Seal`, `Open`, `Destroy`, and the acquire / load / create / unwrap path |
| `subjectbox.go` | the subject box layout, `SubjectOf`, the length-prefixed binding, the wrap's associated data |
| `subjectcache.go` | `openedKey` (copies out, wipes in place) and `keyCache` (the `kernel/collections/cache` primitive plus the destruction epoch) |
| `subjectrewrap.go` | `Rewrap` / `RewrapValue` and `OldestRoot` |
| `subjectmemory.go` | `NewMemorySubjectKeyStore` — the reference `core/security/secret.SubjectKeyStore` |
| `rotator.go` | `Rotator` / `NewRotator` / `RotatorConfig` / `PolicySpec` / `Random` |
| `keyfile.go` | `KeyFile` — the machine-local key for a sealed file store |
| `wrap.go` | `wrapAs` (a core sentinel as the origin, a cause as a field), and `storeFailure` (a `SubjectKeyStore` error as `StoreUnavailable`, the store's own error kept) |

## Decisions worth not relitigating

- **Environment: both `NAME` and `NAME_FILE` set is `ENV_REFUSED`**, never a
  precedence rule — the official container images refuse it too. One trailing
  line ending (`\n` or `\r\n`) is removed from a file; an empty variable is
  unset; an empty or >64 KiB file is refused; an unreadable file is the
  retryable `StoreUnavailable` (a volume may not be mounted yet). A name whose
  variable ends in `_FILE` is refused by this store. `Names` skips an empty
  variable, in either form, since `Get` treats it as unset. No refusal repeats a value
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
- **The directory is judged twice**: by path in `prepareDir`, then — after
  `vfs.NewOS` and the locker have resolved the path again — on the HELD root's
  handle, whose mode must still be owner-only and which `os.SameFile` must
  still match the path (`checkHeldRoot`). A path swapped between the check and
  the open is refused rather than served.
- **The platform gate is decided before the directory is created**
  (`fileguard_*.go`, the same tag set as `vfs`'s `osguard_*.go`).
- **Keyring box**: `0x01 | uint32 BE version | crypto box`; signature
  `0x01 | version | 32-byte HMAC`. The binding (AAD / signed prefix) is
  `"kitsunium/secret keyring v1\x00" + name + "\x00" + version`, then the
  caller's bytes. A version must be exactly one `crypto.Key` long; HKDF-SHA256
  with two labels makes its AEAD key and its MAC key two keys, and a third
  (`kitsunium/secret keyring wrap v1`, ADR 0142) the key subject data keys are
  wrapped under — `sealAs` / `openAs` / `view` take the label. `Open`/`Verify`
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
  processes; the mutex serialises within one. The optional `InUse` (ADR 0142)
  is asked after the `Put` and before the `Prune`: the prune keeps
  `max(Keep, newest − oldest + 1)`, so a version a data key is still wrapped
  under is never pruned, and an `InUse` error prunes NOTHING — unknown is never
  read as "none".
- **Subject keys (ADR 0142)**: a data key is 32 bytes from `crypto/rand`,
  wrapped under the root's newest version with the keyring's WRAP key — never
  its `Seal` key, so no value a caller seals directly under the same root can
  pass for a data key — and the associated data
  `"kitsunium/secret subject key v1\x00" + subject` (so a wrapped key filed
  under another subject does not unwrap). It is never a cipher key itself: HKDF
  derives the AEAD key and an 8-byte identifier from it under two labels bound
  to the subject. Box: `0x01 | len(subject) | subject | id(8) | crypto box`,
  associated data `"kitsunium/secret subject box v1\x00" + header +` each bind
  part behind a 4-byte big-endian length. `Open` answers `KEY_DESTROYED` for a
  box whose key is not held (absent, or held under another identifier: the
  subject came back with a new key), `SEAL_INVALID` for every box-shaped
  failure, `SUBJECT_KEY_UNREADABLE` for a key held that does not unwrap (a
  fault — never an erasure — which `Seal` never overwrites), and
  `STORE_UNAVAILABLE` when the store or the root store failed. A first `Seal`
  inserts; a lost insert race re-reads, three turns at most. After its insert
  it reads the root's newest version once (`settle`) and, when the root
  rotated since the wrap, re-wraps its own key there with a compare-and-swap:
  a rotation that scanned before the insert could not see the key and may
  prune the version it was wrapped under, and every rotation stores its version
  before it scans, so that one read sees every such rotation.
- **The cache and the erasure**: an `openedKey` hands out COPIES under its
  mutex and is wiped under the same mutex, so no call seals under bytes an
  eviction zeroed. `Destroy` deletes from the store FIRST, then advances the
  epoch and wipes the entry; a fill reads the epoch before the store and
  caches only if it did not move, so a fill that read the key before the
  deletion never re-caches it. A cached key whose identifier a box does not
  match is dropped and the store read once (another process replaced it).
  `CacheTTL` is required with a cache and refused without one: it bounds how
  long a key destroyed by ANOTHER process keeps opening — and sealing — here.
- **Rewrap**: reads the root once (`rootView`) and OPENS every key
  (`unwrapIn`: grammar, version held, authenticated, one key long); moves each
  key not under the newest version with a compare-and-swap `Replace` (a key
  destroyed meanwhile is skipped, never written back); counts a key under a
  version NEWER than the pass read as current; counts everything that does not
  open as unreadable and returns `SUBJECT_KEY_UNREADABLE` after finishing the
  pass. `OldestRoot` opens every key too and counts only those that open — a
  key lost with a pruned version, altered, or no keyring box pins nothing, or
  the root would never prune again — checks the context at every key, and
  fails closed.

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
| `NewSubjectKeys(SubjectKeysConfig) (*SubjectKeys, error)` | `Seal(ctx, subject, plaintext, bind...)` / `Open(ctx, box, bind...)` / `Destroy` / `Rewrap` / `OldestRoot` |
| `SubjectKeysConfig` | `Root`, `Store`, `CacheSize`, `CacheTTL`, `Clock` |
| `RewrapValue` | `Root`, `Current`, `Rewrapped`, `Skipped`, `Unreadable` |
| `SubjectOf(box) (string, error)` | the subject a box names, without opening it |
| `NewMemorySubjectKeyStore() core/security/secret.SubjectKeyStore` | the reference store; cannot fail |
| `RotatorConfig.InUse` | `func(ctx) (oldest int, err error)` — wire `SubjectKeys.OldestRoot` |

## Tests

`store_external_test.go` runs one conformance table against memory, file and
sealed file (the file stores join wherever a probe `NewFile` does not answer
`UnsupportedPlatform`). The environment tests call `t.Setenv` and therefore do
not run in parallel. The subject keys are covered by `subjectkeys_`,
`subjectrewrap_`, `subjectfaults_` and `subjectmemory_external_test.go` (two
engines over one store stand for two processes; a scripted store injects
failures and an erasure in the middle of a re-wrap), by
`subjectcache_internal_test.go` (every way a key leaves the cache wipes it) and
by `subjectkeys_internal_test.go` (a value wrapped under the root that is not
one key long is unreadable: never used as a key, never moved, pinning no
version).
`subjectkeys_bench_test.go` feeds `BENCH.md`.

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
