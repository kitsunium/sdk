# Notes — <!-- updated: 2026-10-03T07:14:15Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Decisions worth not relitigating

- **Environment: both `NAME` and `NAME_FILE` set is `ENV_REFUSED`**, never a
  precedence rule — the official container images refuse it too. One trailing
  line ending (`\n` or `\r\n`) is removed from a file; an empty variable is
  unset; an empty or >64 KiB file is refused — judged on the raw bytes read,
  BEFORE the line ending is trimmed: the read stops at 64 KiB + 3 bytes, and a
  read that reaches it is refused, so a longer file is never truncated into a
  secret (#435); an unreadable file is the
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
  is asked after the `Put` and before the prune, and an `InUse` error prunes
  NOTHING — unknown is never read as "none". The prune is by version NUMBER
  (#434): it drops every version below `min(created − Keep + 1, oldest in
  use)`, `created` being the version THIS rotation stored, through the store's
  `FloorPruner` (memory, file and env stores all have it). A count ("keep the
  newest N") is decided when the prune runs, so a version another process Put
  in between pushed one in use out of the window; a floor cannot be moved by a
  newer version. A third-party store without `FloorPruner` is pruned by a
  count re-read just before the `Prune`, which narrows that window without
  closing it — the `Locker`, and no manual `Put` during rotations, close it.
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
  Every lookup first sweeps the cache (`kcache.Cache.Expire`, #1038): a key
  whose `CacheTTL` passed is wiped by the next call on ANY subject, not kept
  until its own subject is read again. A process that makes no call at all
  is the caller's to sweep: `SubjectKeys.Expire` (#1038, option (b), the
  owner's choice on 2026-11-03, ADR 0142 amendment) wipes every opened key
  past its TTL through the same `OnEvict` and reports how many, reads no store,
  and costs nothing when no key is due. The engine owns no goroutine and no
  `Close`: the caller drives it, `worker.Every(ttl/2, …)` wiping an idle key
  within 1.5 × `CacheTTL`. A swept key stays in the store, so the next call on
  its subject reads and opens it again
  (`TestExpireWipesAnIdleSubjectsKeyWithNoCallAtAll`).
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
- **A root renamed** (`SubjectKeysConfig.Former`, ADR 0180 step 5b): a
  keyring binds its secret's NAME into every box and wrapped key, so a root
  secret renamed is a new keyring that opens nothing the old one wrapped.
  `Former` is that old keyring: `unwrap` tries it after the root (never on a
  `STORE_UNAVAILABLE`), nothing is ever wrapped under it, and `Rewrap` reads
  it once (`formerView`; a former name with no version is none) and moves
  every key only it opens to the root's newest version — counted in
  `Rewrapped` and in `FromFormer`. A key under a version newer than the pass
  read is still current, but only once neither the root's view nor the
  former's opened it. `OldestRoot` ignores a key under `Former`: it pins none
  of the root's versions. Nothing here rotates, prunes or deletes the former
  secret; retiring it is the caller's, once a pass finds nothing under it.
  A root given as its own `Former` is refused (`INVALID_CONFIG`).
- **`OldestRoot` stays a scan, measured** (#274 Q1, the owner's delegation of
  2026-10-15; ADR 0178, decisions of 15 October). No index of the oldest
  version is kept: an index cannot know which keys still OPEN without opening
  them, which is the one thing `OldestRoot` answers, and the scan runs once per
  rotation of the root — kit's `data-key` every 30 days, from the rotator's
  `floorFor`. `BenchmarkSubjectKeysOldestRootPopulation` measures it, the keys
  over two root versions as during a re-wrap, memory store, median of five on
  a shared 8-core AMD EPYC 7351P (linux/amd64, go1.27.2, load 5 to 8):

  | subjects | ms/pass | ns/key | B/op | allocs/op |
  |---|---|---|---|---|
  | 1 000 | 3.1 | 3 104 | 1.6 MB | 7 065 |
  | 10 000 | 41 | 4 110 | 16.7 MB | 70 072 |
  | 100 000 | 389 | 3 893 | 169.7 MB | 700 083 |

  Linear: about 4 µs a key here (0.8 µs on the M1 Pro of `BENCH.md`), so a
  million subjects cost about 4 s and 1.7 GB of short-lived allocation once a
  month. The fallback, if a measurement on a real product ever justifies it,
  is kit's and not this package's: remember the version the last `Rewrap`
  left every key under and scan only when that `Rewrap` left keys behind —
  never an index here.
