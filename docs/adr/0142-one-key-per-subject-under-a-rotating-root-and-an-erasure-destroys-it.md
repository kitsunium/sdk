# ADR 0142 — one key per subject, under a root that rotates, and an erasure is the key's destruction

- **Status**: Accepted
- **Date**: 2026-09-27
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0096](0096-a-secret-is-a-value-no-rendering-writes-down.md) §6 (a rotation's prune honours `RotatorConfig.InUse`)
- **Related**: [ADR 0013](0013-sdk-crypto-domain.md) (the AEAD every box is sealed with), [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md) (the KDF port, and `KeyEnvelope` / `KeyTree`, which this is not), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the frozen port), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the shapes it publishes), [ADR 0120](0120-a-state-machine-keeps-an-agenda-not-a-sweep.md) (absence is an answer, `Replace` never resurrects), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the refused cache TTL), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (which layer each alias points at), [ADR 0025](0025-sdk-cache-kernel.md) / [ADR 0049](0049-cache-becomes-a-domain.md) (the cache primitive reused), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the two ranges, both already owned)

## Context

A framework built on the SDK (kit, its ADR 0006) classifies data field by
field and seals the personal ones at rest. Step 3 of that record asks the SDK
for the mechanism, as a record of the `secret` domain, and says what it needs:

- one data key per data subject, wrapped by a root key the framework rotates —
  its generated secret `data-key`, a `Keyring` rotated every 30 days with three
  versions kept (kit ADR 0002);
- a rotation of the root that re-wraps one small key per subject, never every
  field;
- a seal and an open under a subject's key, bound to where the value lies —
  the store, the record's key, the member's JSON pointer;
- the destruction of a subject's key as a cryptographic erase, reaching every
  copy sealed under it — former versions, a dead letter, a replica, a dump;
- the wrapped keys persisted behind a port, with a bounded cache of opened
  keys.

The product that motivated it (Vigie, a DSA moderation service) seals every
field under ONE key read from the environment and never rotated. That shape
fails both halves of the requirement. A rotation that prunes a version must
reseal every field of every store first, so nobody rotates; and erasing a
person rewrites the records the product can find, while the copies it cannot —
a store's former versions, a dead letter, a backup — keep the person in them.

The SDK had the pieces and not the mechanism. The `Keyring` (ADR 0096) turns a
secret's versions into keys and its boxes name the version that sealed them;
the `Rotator` prunes right after it stores a new version; `crypto` seals with
AES-256-GCM and `kdf` separates keys with HKDF-SHA256. `KeyEnvelope` wraps a
key under a PASSPHRASE through PBKDF2 — a human secret, no subject, no store.
`KeyTree` DERIVES keys from a master, and a derived key cannot be destroyed on
its own: it exists as long as the master does.

What NIST calls this, checked on 2026-09-27:

- **Cryptographic erase** (SP 800-88r2, *Guidelines for Media Sanitization*,
  September 2025, glossary): "a purge sanitization technique in which key
  sanitization is applied to one or more keys providing confidentiality
  protections for the encrypted target data, making recovery of the decrypted
  target data infeasible". Its §3.2.3 adds three things this ADR turns into
  mechanism or into a stated bound: when "ciphertext (i.e., wrapped) versions"
  of data-encryption keys are sanitized, "the corresponding ciphertext cannot
  be decrypted"; "all copies of the target cryptographic keys must be able to
  be sanitized" (after ISO/IEC 27040); and "if the wrapped versions of keys had
  previously been unwrapped and the keys inside them stored to volatile
  memory", eliminating those unwrapped copies "must also be assured". §3.2.2:
  CE "should not be trusted on ISM that have been backed up or escrowed unless
  the organization has a high level of confidence regarding how and where the
  keys were stored".
- **Wrapping with GCM** (SP 800-38F, *Methods for Key Wrapping*, §3.1): "there
  is no requirement to protect cryptographic keys with a distinct cryptographic
  method. Previously approved authenticated-encryption modes […] are approved
  for the protection of cryptographic keys, in addition to general data."
- **The GCM invocation bound** (SP 800-38D §8.3): with random IVs, "the total
  number of invocations of the authenticated encryption function shall not
  exceed 2^32 […] with the given key". Go's `crypto/cipher` states the same for
  `NewGCMWithRandomNonce`: "A given key MUST NOT be used to encrypt more than
  2^32 messages".

## Decision

The mechanism lands in the `secret` domain, in the two packages that already
own its ranges: the port in `internal/core/secret`, the engine in
`internal/service/secret`, the facade in `pkg/v1/secret`. It is a toolbox
mechanism: which fields are sealed and who the subjects are stays the caller's.

### 1. A subject is a lowercase reference, never an identity

A subject names whoever or whatever a data key belongs to — a person, a
tenant, a record. It is the KEY the caller's store files a wrapped key under
and a field of every box, kept in clear in both, so it must be a reference
derived from an identity (an HMAC of it, in hexadecimal), never the identity.
`ValidateSubject` accepts 1–128 bytes of `a-z 0-9 - _ . :`, first byte a letter
or a digit — room for a SHA-512 in hex — and refuses the rest with
`INVALID_SUBJECT` (`0.2.37.8`), never echoing the string: the one most likely
to be refused is the e-mail address that should have been hashed.

LOWERCASE is the load-bearing clause. Several SQL engines collate
case-insensitively by default; a store on one would file `Ab` and `aB` as one
subject, and two subjects sharing one data key erase each other. The secret
name grammar is lowercase for the same reason on filesystems.

### 2. The port is the caller's, frozen at five, and two of its methods are atomic

`SubjectKeyStore` — `Get`, `Insert`, `Replace`, `Delete`, `All` — keeps one
wrapped key per subject. It is frozen under ADR 0039 and follows ADR 0120's
convention: absent and taken are ANSWERS with a nil error (`found`,
`inserted`, `replaced`, `deleted`), so an error always means the store failed.
The caller implements it over its own storage; `NewMemorySubjectKeyStore` is
the reference implementation and the one tests use.

Two methods must be atomic across every process sharing the storage, and the
port's documentation says what breaks otherwise, because a store that cannot
keep them loses data rather than failing:

- `Insert` is insert-if-absent. Two first writers of one subject otherwise
  both file a key, and every box sealed under the one overwritten opens
  nowhere. The engine re-reads after a lost race, three turns at most.
- `Replace(subject, current, next)` is compare-and-swap on the exact bytes. A
  re-wrap that read a key an erasure then destroyed otherwise writes it back,
  and the erasure is undone.

`All` yields every key and holds no lock across a yield, because the re-wrap
replaces keys while it ranges. `Delete` is the destruction: the key must leave
the storage the store reads.

### 3. A data key is random, wrapped under the root's newest version, and bound to its subject

A data key is 32 bytes from `crypto/rand`, made by the first `Seal` for its
subject and wrapped under the root keyring's newest version — its number in
the header, as in every keyring box — with the associated data
`"kitsunium/secret subject key v1\x00" + subject`, behind the keyring's own
binding of its name and version. A wrapped key filed under another subject does
not unwrap. Wrapping is AES-256-GCM, which SP 800-38F §3.1 approves for keys;
AES Key Wrap (RFC 3394, SP 800-38F's KW) is not in Go's standard library, and
GCM binds the subject as associated data where KW has no place for it.

The wrap does NOT use the key the keyring's `Seal` uses. The keyring already
splits each version with HKDF into a sealing key and a signing key; a third
label, `kitsunium/secret keyring wrap v1`, derives the key data keys are
wrapped under. A framework also seals values directly under the same root (kit
seals a message that has no subject under `data-key` itself), and with one
shared key only the associated data would keep such a box from passing for a
wrapped key — a caller that ever sealed 32 chosen bytes with the wrap's
associated data would have planted a key it knows. With a key of its own, no
`Seal` box unwraps as a data key, whatever it carries.

The data key is never used as a cipher key. HKDF-SHA256 derives from it the
AEAD key its boxes are sealed with and an 8-byte key identifier, under two
labels each followed by NUL and the subject; the data key is cleared as soon as
both are derived. One root version wraps each subject's key about once — at its
creation or at its re-wrap — so SP 800-38D's 2^32 bound on one key is a bound
on subjects per root version, not on writes; one subject's key seals only that
subject's values, far below it.

### 4. A box names its subject and its key, and is bound to where it lies

```
box = 0x01 | len(subject), 1 byte | subject | key identifier, 8 bytes | crypto box
```

The subject is length-prefixed and the identifier fixed-width, so a header
reads one way. The associated data is a domain string, the whole header — so
neither the subject nor the identifier can be edited — then each part the
caller binds, behind a 4-byte big-endian length (the `KeyTree` encoding):
`Seal(ctx, subject, plaintext, "reports", id, "/email")` opens only with the
same parts, in order, and `("ab", "c")` differs from `("a", "bc")`. A box
copied into another record or field does not open.

The box carries its subject because an erasure must learn which keys the
records it erases were sealed under — `SubjectOf(box)` reads it without
opening — and because a record re-sealed under a key of its own (a held record
moved away from its person before the person's key is destroyed) is `Open`
then `Seal` under another subject.

The identifier is what tells two keys of one subject apart. A subject erased
and written again has a NEW key; a box of the old one names an identifier the
store no longer holds, and reads as erased, not as tampered.

### 5. `Destroy` is the erasure, and what it reaches is stated

`Destroy(subject)` deletes the key from the store and reports whether there
was one; a subject without a key is not an error. Every box sealed under the
key stops opening wherever it was copied — former versions, a dead letter, a
backup of the DATA — because nothing holds the key: the cryptographic erase of
SP 800-88r2. What it reaches, and how fast, is part of the contract:

- **this process**: at once. The cache drops the key and wipes it (§7), and a
  fill that read the key before the deletion never re-caches it;
- **another process** that cached the key: within its `CacheTTL`;
- **a copy of the WRAPPED key** that the key store's backend kept elsewhere — a
  write-ahead log, a replica, a backup of the KEY store: it is wrapped under
  the root version that sealed it, and opens only while that version is kept.
  The erasure is complete everywhere once that version is pruned, which with a
  30-day rotation keeping three versions is 90 days at most — and only if the
  backup does not also hold the root secret's own store, which is why a root
  kept beside the data protects against a leak of the stores and not of the
  whole data directory.

### 6. A rotation re-wraps one key per subject, and never prunes a version a key needs

`Rewrap` reads the root once — every usable version's wrap key derived once —
and OPENS every key: one not under the newest version is wrapped again and
filed with a compare-and-swap `Replace`, so a key destroyed or re-wrapped
meanwhile is skipped and never written back. No box is touched. A key already
under the newest version is opened too, and so is proven current rather than
assumed from its header: the report's `Unreadable` counts every key that does
not open as one data key — a version gone, another purpose, altered, not one
key long — and the pass finishes before returning `SUBJECT_KEY_UNREADABLE`. A
key filed under a version NEWER than the one the pass read — a rotation and a
first `Seal` landed after the pass began — is current, and the next pass's to
open. It is idempotent, so it runs after every rotation and at start-up, to
finish a pass that was interrupted.

The `Rotator` prunes right after it stores a new version (ADR 0096 §6). With
keys still wrapped under the version a prune would take, that prune is an
ACCIDENTAL erasure of every subject whose re-wrap lagged — the loss the whole
mechanism exists to control. kit's record proposed that the framework refrain
from rotating while a key is under the version the next rotation would prune.
The SDK closes it where the prune happens instead: `RotatorConfig.InUse`, a
`func(ctx) (oldest int, err error)` — `SubjectKeys.OldestRoot` has that
signature — is asked after the new version is stored and before the prune,
which then keeps `max(Keep, newest − oldest + 1)`. `Keep` becomes a floor: the
root keeps rotating, and the old version lingers until the keys move on. An
`InUse` error prunes NOTHING and is returned, because "unknown" read as "none"
destroys what depends on a version.

`OldestRoot` reads the root once and opens every key under the version its
header names, and counts only the keys that open. A key that does not — its
version already pruned, altered, not a keyring box — is lost whatever is kept,
and counting it would pin its version and every later one for ever: the root
would never prune again, and a backed-up wrapped key would keep opening long
after its subject's erasure, which is §5's bound broken by a single bad
record. Such keys are `Rewrap`'s to report. A cancelled scan stops at the next
key, so a rotation given a deadline does not read a million keys past it.

The scan sees every key FILED when it runs, and one kind of key is not: a key
a first `Seal` has wrapped and not yet inserted. A rotation that scans in that
window, after another that retired nothing, prunes the version the key was
wrapped under, and the insert would then file a key nothing opens. `Seal`
closes the window without any lock: after its insert it reads the root's
newest version once, and when the root rotated since the wrap, it wraps the
data key — still in hand — under the newest version and files it with a
compare-and-swap. That single read is enough because every rotation stores its
new version BEFORE it scans: a rotation that could have missed the key has
stored its version before the insert, so the read sees it, and the version it
reads is one every such rotation keeps; a rotation that stores its version
after the read scans after the insert, and sees the key. `Rewrap` needs no
such step: the key it rewrites stays filed throughout, under a version no newer
than the one it writes, so every scan sees it.

### 7. A bounded cache, whose TTL is a promise about erasure

Opened keys — the derived AEAD key and the identifier, never the data key —
sit in the kernel LRU+TTL cache (ADR 0025). Two rules make it safe:

- **copies out, wipe in place**. An opened key may be shared by every call
  that finds it, so it hands each call a COPY under its mutex, and is wiped —
  cleared, not merely dropped — under the same mutex on eviction, expiry or
  destruction. A cache that handed out its own bytes would let an eviction zero
  a key another goroutine is sealing with, and that box would be sealed under
  thirty-two zero bytes;
- **an epoch orders a fill against a destruction**. `Destroy` deletes from the
  store FIRST, then advances the epoch and wipes the entry; a fill reads the
  epoch before it reads the store and caches only if it did not move.

`CacheSize` 0 caches nothing. A positive size REQUIRES a positive `CacheTTL`,
and a TTL without a size is refused (ADR 0031): the TTL is how long a key
another process destroyed keeps opening — and sealing — here, a promise about
erasure only the caller can make. A cached key whose identifier a box does not
match is dropped and the store read once, so a subject erased and written again
elsewhere is read under its new key rather than reported erased.

### 8. Four verdicts, and the one that must never be read as another

| Verdict | Means | The caller |
|---|---|---|
| `KEY_DESTROYED` `0.3.68.9` | the box's key is not held: destroyed, or replaced by a newer key of its subject | reads the value as erased |
| `SEAL_INVALID` `0.3.68.4` | malformed, altered, or bound to other parts | rejects the box |
| `SUBJECT_KEY_UNREADABLE` `0.3.68.10` | a key IS held and does not unwrap: its root version pruned, the root secret replaced, the record altered | alerts — never an erasure; `Seal` never replaces such a key, `Destroy` removes it |
| `STORE_UNAVAILABLE` `0.2.37.4` | the key store or the root store failed | retries |

Reading `SUBJECT_KEY_UNREADABLE` as an erasure would turn a misconfigured root
into a silent loss of every value; reading `STORE_UNAVAILABLE` as one would
erase every value for the length of an outage. A store's own error stays in the
chain: its code when it is an SDK error — origin wins — and `errors.Is` when it
is not.

`KEY_DESTROYED` cannot tell a key destroyed from one never made, nor from a box
whose identifier or subject bytes were edited. Telling them apart would mean
keeping a trace of every erased subject — the reference an erasure removes —
and an attacker able to edit a stored box can already delete the value.

### 9. No new range, no new package

`internal/core/secret` owns `0.2.37.*` and `internal/service/secret` owns
`0.3.68.*` (ADR 0096); the three new codes continue them. The engine lives
beside the `Keyring` it wraps under, which is what lets a re-wrap read the root
once through an unexported view instead of widening the keyring's surface.

## Consequences / Semantics

- A framework seals a member with one call, opens it with one, erases a person
  with one, and rotates its root on the `Rotator` it already runs, wiring
  `InUse: keys.OldestRoot` and a `Rewrap` after each rotation.
- Measured on an Apple M1 Pro, medians of five runs over in-memory stores
  (`internal/service/secret/BENCH.md`): a 64-byte value seals under a cached
  subject key in about 0.9 µs against 0.75 µs for the bare AES-256-GCM seal
  underneath, and opens in 0.6 µs; a key not cached costs one read of each
  store and three HKDF derivations, about 4 µs, once per `CacheTTL`. A
  rotation costs the engine about 2 µs per subject, and the caller's store one
  durable write per subject on top — on a document store on disk, about 11 ms
  here, so a million subjects re-wrap in hours, in the background, while
  `InUse` keeps the old version. `OldestRoot` and a pass with nothing to move
  open every key, about 0.8 µs each.
- The root keeps more than `Keep` versions while a re-wrap lags, and says
  nothing: the store's `Versions` shows it.
- A box is 40 bytes plus its subject longer than the value it seals.
- The domain gains three codes: `0.2.37.8` and `0.3.68.9`–`0.3.68.10`.

## Breaking changes

None. The subject keys are new symbols. `RotatorConfig` gains `InUse`, whose
zero value is the old behaviour; a positional literal of `RotatorConfig` would
break, which ADR 0040 accepts while the module is v0.

## Alternatives considered

- **One key for everything**, as the product does. Every prune reseals every
  field, and an erasure cannot reach a copy nobody rewrites.
- **A key per record.** Every erasure is cryptographic, but a person's erasure
  collects thousands of keys, and a retention rewrite reaches a record's copies
  only when its backups expire anyway.
- **Deriving a subject's key from the root** (`KeyTree`, HKDF over the
  subject). No store at all — and no erasure: the key exists as long as the
  root, and destroying one subject means rotating everybody, which reseals
  everything.
- **AES Key Wrap** (RFC 3394). Not in Go's standard library, no associated
  data, and SP 800-38F §3.1 approves GCM for keys.
- **Tombstones for destroyed subjects**, to tell "destroyed" from "never made".
  A list of everybody erased, kept after the erasure.
- **The subject outside the box**, given to `Open`. The erasure needs to read
  which key a box names, and a re-seal under a record's own key changes it.
- **Refraining from rotating** while a key lags, as kit's record proposed.
  Every caller would rebuild the rotator's loop to hold a rotation back; the
  prune is where the loss happens, so the guard is there.
- **A lock shared by the rotator and every first `Seal`**, to close the
  creation window of §6. It would hold a cross-process lock across a whole
  `InUse` scan — every new subject waiting minutes on a large store — to cover
  a window one read of the root closes, because a rotation stores its version
  before it scans.
- **Wrapping with the root's `Seal` key**, separated from the caller's own
  boxes by the associated data alone. It holds only while no caller ever seals
  under that root with associated data that begins with the wrap's domain
  string; a third HKDF purpose costs nothing and holds unconditionally.
- **Wiping the cached key itself** on eviction, with no copies. A race that
  seals under zeroed bytes, measured nowhere because it corrupts silently.
- **A default `CacheTTL`.** It is the latency of an erasure across processes;
  an SDK-chosen number would be a privacy promise nobody made.
- **Checking the store on every `Seal`**, so a key destroyed elsewhere never
  seals. It doubles the cost of every write to close a window the TTL already
  bounds; a caller who cannot accept the window sets `CacheSize` to 0.

## Deferred

- **A persistent `SubjectKeyStore` in the SDK** — over `docstore` or `sql`. A
  framework implements the port over its own stores; a shipped adapter waits
  for a caller who has none.
- **An exported conformance suite** for implementers of the port, as the
  memory store's tests are today.
- **An indexed `OldestRoot`** — a sibling interface answering the oldest root
  version without a scan; today a rotation reads every header once.
- **A parallel re-wrap**, and **collapsing concurrent misses** on one subject
  (the kernel `singleflight`): measured first.
- **Streaming seal under a subject key**, for values too large for one box.
- **Re-keying a subject** — a new data key for a living subject and a re-seal
  of what the old one sealed.
- **A root held in a KMS**, a `Store` or a `Keyring` behind a connector.
- **A cross-process invalidation** that ends a destroyed key's cache life
  sooner than its TTL.

## Verification

- `internal/core/secret`: the port frozen at five; the subject grammar's
  accepted and refused sides, and no refusal repeating its input.
- `internal/service/secret`: the round trip and the box saying nothing of its
  plaintext; every binding mismatch (another record, field, store, order,
  arity, splitting); every altered box refused, with the two verdicts the doc
  names; an erasure reaching three copies of one subject's boxes and no other
  subject's, and a subject written again under a new key; the erasure reaching
  its own process's cache at once and another process's at its TTL, to the
  second on a manual clock; a cached key replaced elsewhere read again; 32
  first `Seal`s across two processes filing one key; a replaced root, a pruned
  version, a short unwrapped key and a box the root sealed with `Seal` planted
  as a key each `SUBJECT_KEY_UNREADABLE`, never overwritten; a rotation moving one key per subject and never touching a box;
  `InUse` keeping version 1 through three rotations and pruning back to `Keep`
  after the re-wrap, an `InUse` error pruning nothing; an erasure landing in
  the middle of a re-wrap not undone; a restored backup of the key store
  opening while its version is kept and never after; store and root outages
  staying `STORE_UNAVAILABLE`; every way a key leaves the cache wiping it; a
  first `Seal` whose insert stalls across two rotations — the second pruning
  the version it wrapped under — still filing a key that opens; `OldestRoot`
  pinning nothing for a key lost with a pruned version, an altered key or a
  record that is no keyring box, the root then pruning back to `Keep` through
  three rotations; a cancelled scan stopping at the second of three keys; a
  re-wrap counting an altered key under the newest version, and a key that is
  not one key long, as unreadable, and a key newer than the pass as current.
  Mutations checked against the suite — the binding ignored, the length prefix
  dropped, `Destroy` not reaching the cache, no re-read of a replaced key,
  `InUse` ignored, `Replace` without its compare, the wrap under the `Seal`
  key, no settling after a first insert, no length check, a current key taken
  from its header, `OldestRoot` counting by header, no cancellation check per
  key, a newer key read as unreadable — each fail it.
- `pkg/v1/secret`: the wiring the package doc shows, through public names only.

## References

- `internal/core/secret/CLAUDE.md`, `internal/service/secret/CLAUDE.md`,
  `internal/service/secret/BENCH.md`, `pkg/v1/secret/README.md`
- kit (kitsunium/platform) ADR 0006, §4 and step 3; ADR 0002 (the `data-key`
  secret and its rotation)
- Sources, consulted 2026-09-27:
  - NIST SP 800-88r2, glossary, *cryptographic erase*:
    https://csrc.nist.gov/glossary/term/cryptographic_erase
  - NIST SP 800-88r2, §3.2 (use of cryptography and cryptographic erase):
    https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-88r2.pdf
  - NIST SP 800-38F, §3.1 (authenticated-encryption modes approved for keys):
    https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-38F.pdf
  - NIST SP 800-38D, §8 and §8.3 (IV uniqueness, 2^32 invocations):
    https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication800-38d.pdf
  - Go `crypto/cipher` (`NewGCMWithRandomNonce`, the 2^32-message limit),
    `crypto/hkdf` (`Key`), `crypto/rand` (`Read` never fails, and fills `b`
    entirely), `crypto/subtle` (`ConstantTimeCompare`):
    https://pkg.go.dev/crypto/cipher, https://pkg.go.dev/crypto/hkdf,
    https://pkg.go.dev/crypto/rand, https://pkg.go.dev/crypto/subtle
  - RFC 5869 (HKDF), RFC 3394 (AES Key Wrap)
