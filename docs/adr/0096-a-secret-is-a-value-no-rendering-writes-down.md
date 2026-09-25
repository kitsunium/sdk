# ADR 0096 — a secret is a value no rendering writes down, a name with versions, and a key that rotates without breaking what it sealed

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Related**: [ADR 0013](0013-sdk-crypto-domain.md) (the AEAD the keyring seals with, and the redacting `Key` precedent), [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md) (MAC and KDF ports), [ADR 0045](0045-sdk-session-domain.md) (the redacting `ID` and the sealed file store this follows), [ADR 0052](0052-sdk-lock-domain.md) (the file locker that serialises writers), [ADR 0056](0056-sdk-vfs-domain.md) (atomic publication), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock a rotator is tested on), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (every refused zero value below), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the frozen `Store`), [ADR 0032](0032-logger-slog-bridge.md) (why the value has no `LogValue`), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the two ranges), [ADR 0097](0097-config-provenance-and-secret-fields.md) (the configuration half: a secret field is marked in a traced load, and loaded from the environment as written)

## Context

The SDK already knew that some values are secrets. `crypto.Key` and
`session.ID` render as `<redacted>` and hand their bytes out through one named
method each; `mail.SMTPConfig` promised in a comment that its password "never
appears in an error, a field or a log line emitted by this package". What it did
not have was a type for the secret itself. A database URL, an API token, an SMTP
password or an HMAC key arrived in a `string` or a `[]byte`, and every one of
them was one `%+v` away from a log line, one `json.Marshal` away from a
`--show-config` dump, and one `config.Load` away from sitting in a struct nobody
had marked.

A framework built on the SDK showed the other three gaps at once, because it
re-implemented each of them: where a secret comes from (the process environment,
and the `NAME_FILE` convention Docker and Kubernetes mount secrets through), where
a secret the process generated itself is kept (a directory, readable by nobody
else, written atomically), and how a key rotates without logging out every
session that was sealed a minute before the rotation.

Those are four mechanisms, and each has a way of being done wrong that looks
right: a redaction that only covers `String()`, a secret file read with a
trailing newline, a store written in place, a rotation that replaces the only key.

## Decision

A new domain, `secret`: core slot `0.2.37.*` (`0x00_02_25_*`), service slot
`0.3.68.*` (`0x00_03_44_*`), `internal/core/secret` (the value, the port, the
grammar), `internal/service/secret` (three stores, the keyring, the rotator),
`pkg/v1/secret` (aliases and forwarders).

### 1. A `Value` renders as one placeholder in every rendering there is

`secret.Value` implements `String`, `GoString`, `Format`, `MarshalJSON` and
`MarshalText`, and every one writes `<redacted>` — the same ten characters the
SDK's other redacting types write, for the empty secret too, because a rendering
that varied with the content would say whether a secret is set and, with `%x`,
how long it is. `Format` is the method that matters most and is the one usually
forgotten: without it `%x` and `%d` format the struct's fields.

`Reveal()` (a copy) and `RevealString()` are the only exits. `Equal` compares in
constant time, and `==` does not compile: the struct carries a zero-size field of
a non-comparable type, so neither the byte comparison that leaks a shared
prefix's length nor a pointer comparison that answers "same allocation" can be
written — and a `Value` cannot be a map key.

The bytes sit behind a pointer. fmt cannot call methods on a value it reaches
through an UNEXPORTED struct field, and prints that field's own fields by
reflection instead; a slice there would print the secret as a list of byte
values, a pointer prints an address. This is tested, both ways: an exported
field prints the placeholder, an unexported one prints neither the secret nor
the placeholder.

It has no `LogValue`. `slog.LogValuer` would put `log/slog` in `internal/core`,
which ADR 0032 forbids, and it is not needed: slog's text handler reaches for
`encoding.TextMarshaler`, its JSON handler for `json.Marshaler`, and the bridge
converts an `Any` with fmt — all three write the placeholder, and a test in
`pkg/v1/logger/slogbridge`, the one package allowed to name slog, asserts it.

### 2. A `Value` decodes from a string and from nothing else

`UnmarshalJSON` and `UnmarshalText` make a `Value` field fill from a
configuration exactly as a `string` field does. Two inputs are refused with
`VALUE_REFUSED`. A JSON token that is not a string: a number has been re-spelled
before any decoder sees it — `1e3` arrives as `1000`, a twenty-digit token loses
its tail to `float64` — so accepting one stores a secret nobody wrote. And the
placeholder itself: every rendering writes it, so finding it on the way IN means
a dumped configuration was loaded as a real one, and every secret in it would
silently have become `<redacted>`. A `null` leaves the field untouched, which is
`encoding/json`'s own convention for a field that decodes itself.

### 3. `Store` is five methods over numbered versions, and frozen

`Get` (the newest), `Versions` (newest first), `Put` (a new version), `Prune`
(keep the newest N, N ≥ 1) and `Names` (never values). Versions number from 1,
strictly increase, and are never reused — not after a prune, not after a restart —
because a version number is how a sealed box names its key, and a reused number
would hand an old box to a new key. `Put` refuses an empty value (`EMPTY_VALUE`)
and `Prune` refuses to keep fewer than one (`INVALID_KEEP`): deleting a secret is
a different decision from pruning it, and must never be reached by arithmetic.
The port is frozen under ADR 0039; a delete, or a conditional put, is a future
sibling.

A name is 1–63 characters of `a-z`, `0-9` and `-`, starting and ending with a
letter or digit — the DNS label grammar — and every store validates with the one
function. The alphabet is closed because a name crosses three naming systems: it
is a file name (no separator, no leading dot), it must be lowercase (macOS and
Windows ship case-insensitive filesystems, so `Key` and `key` would be one file
and two secrets elsewhere), and it maps to an environment variable by
upper-casing and turning `-` into `_` — one-to-one only because `_` is not in the
alphabet. A refused name is never echoed: the classic invalid name is a value
passed where a name belonged.

### 4. Three stores, and what each cannot do is its own verdict

- **Memory**: a map, for tests, development, and a secret regenerated at every
  start.
- **Environment**, read-only: `smtp-url` under prefix `APP` is `APP_SMTP_URL`,
  or the content of the file `APP_SMTP_URL_FILE` names. Both set is refused
  (`ENV_REFUSED`) rather than resolved by a precedence rule — exactly as the
  official container images refuse it, because the operator meant one of them.
  One trailing line ending is removed from a file (a file written with `echo`
  has one; a password does not). An empty variable counts as unset, which is
  what shells and compose files mean by it; an empty FILE is refused, because a
  file named explicitly and found empty is a mount that did not populate. Every
  secret is version 1; `Created` is the file's modification time, or zero for a
  variable. `Put` and `Prune` are `READ_ONLY`. A name whose variable already
  ends in `_FILE` is refused by this store: it cannot be told apart from the
  file form of a shorter name.
- **File**: a directory the store owns — created `0700`, and REFUSED, never
  narrowed, if it exists with a group or world bit. One record per secret, the
  whole history in one JSON document, published with `vfs.WriteAtomic` at
  `0600`, so a reader in any process sees the history before or after a write
  and never a torn one. Writers of one secret are serialised by the lock
  domain's file locker, across goroutines and processes, because `Put` reads the
  history to number the new version. Reads take no lock: the rename is the
  synchronisation. With a `crypto.Key`, every record is sealed with AES-256-GCM
  and bound to its name before it touches the disk, so nothing readable is
  written — not the values, not the numbers, not the timestamps; a record
  copied under another name, written under another key, or written without one
  is `RECORD_UNREADABLE`, never guessed at. The file store refuses at
  construction with `UnsupportedPlatform` wherever `vfs.NewOS` does — today every
  platform outside the Unix family — before creating the directory.
- **The key a sealed file store opens with** comes from `KeyFile(path)`: the
  file holds exactly 32 RAW bytes, and anything else — base64 text, a trailing
  newline, a short file — is `KEY_FILE_INVALID` (`0.3.68.8`), never reshaped,
  because a key reshaped to fit is another key and the store it sealed would
  read as corrupt. An absent file is created on first use: crypto/rand bytes in
  a flushed 0600 temporary beside it, PUBLISHED with a hard link that the
  kernel refuses when the name exists, so processes racing on first use cannot
  both win — the loser reads the winner's file and every caller returns the
  same key; the directory is made 0700 when absent and flushed after the link.
  An existing file with a group or world bit is refused as the store's
  directory is (`INVALID_CONFIG`), and the same platform gate applies.

### 5. A keyring is the versions of one secret seen as keys

The newest version seals (AES-256-GCM through `crypto`) and signs (HMAC-SHA256
through `mac`); every KEPT version still opens and verifies; a version stops
opening only when it is pruned. The SDK's crypto box carries no key identifier,
so the keyring prefixes one:

```
box       = 0x01 | version (4 bytes, big-endian) | crypto box
signature = 0x01 | version (4 bytes, big-endian) | HMAC-SHA256 tag (32 bytes)
```

One fixed width is one spelling per box, which a varint would not be. The
version is not secret; it names a key. The associated data of every box and the
bytes of every signature begin with a binding — a domain string, the keyring's
name NUL-terminated (NUL is not in the name alphabet), the version — so a box
sealed under keyring `a` does not open under `b` even when the two hold the same
bytes, and a header's version cannot be edited.

A version must be exactly one `crypto.Key` long (32 bytes); anything else is
`KEY_MATERIAL_INVALID` rather than stretched, because a password stored under a
keyring's name is not random and stretching would hide it. Each version is split
by HKDF-SHA256 into an AEAD key and a MAC key with two labels, so one 32-byte
value is never both an AES key and an HMAC key.

`Open` and `Verify` answer every box-shaped failure — malformed, unknown format,
a version no longer kept, tampered, other associated data — with one verdict,
`SEAL_INVALID` or `SIGNATURE_INVALID`. A store that could not be READ is the one
failure reported as itself (`STORE_UNAVAILABLE`), because "retry" and "reject the
box" are different responses and only that distinction is safe to make.

### 6. Rotation keeps at least two versions, and starts no goroutine

`Policy{Every, Keep, Generate}`: every field is required and none is defaulted
(ADR 0031) — an interval of zero would rotate on every call, `Keep` below 2
would retire the replaced key at the instant of the rotation so every box sealed
a moment earlier stops opening, and there is no secret the SDK could invent.
`Random(n)` is the generator, `crypto/rand`, refusing below 16 bytes.

`Ensure` creates version 1 when absent. `Due` is the current version's `Created`
plus `Every`. `RotateIfDue` decides and rotates as one step; `Rotate` rotates now;
both generate, `Put`, then `Prune(Keep)`. A rotation whose prune fails returns
the new version AND the error: the rotation happened, `OnRotate` is told, and
the next rotation prunes again. `OnRotate` runs after every lock is released, so
it may call back in; creating version 1 is not a rotation and does not call it.

The rotator takes an injected `clock.Timed` and is tested on a `ManualClock`. It
starts nothing: `Run(ctx)` is a loop that blocks on the caller's goroutine,
sleeps on the clock until the next due instant, and returns `nil` when `ctx`
ends and the first error otherwise — a supervisor retries, a loop does not spin.
An optional `lock.Locker` serialises rotations across processes: without it,
two replicas sharing a file store can both find a rotation due and both perform
it — nothing breaks, both versions are kept keys — and with it, the second
re-reads and finds nothing due.

## Consequences

- Downstream code can hold a secret in a configuration struct, print it, dump
  it and log it without writing it down, and `config.Load` fills it from a
  file or the environment like any string field.
- A deployment can mount secrets the orchestrator way, keep generated ones in a
  sealed directory, and rotate signing keys on a schedule, all behind one port
  that a Vault or KMS connector can implement later without the reading code
  changing.
- Two ranges are claimed: `0.2.37.*` with seven codes (the port's verdicts) and
  `0.3.68.*` with eight (the engines' and the key file's), recorded in
  `codeRangeOwners` in this change.
- The file store and the keyring read the store on every call. A keyring over
  the file store therefore reads one small file per `Open`; caching a version
  would need a cache invalidated by a `Put` in another process, which the port
  cannot announce. The cost is one small file read per call; a caller for whom
  it matters holds a memory store it refreshes itself.

## Breaking changes

None. `secret` is a new domain in this change set; nothing existing changed
shape.

## Alternatives considered

- **`secret.Value` as a `string` newtype** (`type Value string`). Rejected: a
  string newtype converts back with `string(v)` at any call site and is printed
  by `%s` through its underlying type whenever a method is not reached, which is
  exactly the unexported-field case.
- **A `LogValue` method.** Refused by ADR 0032, and unnecessary: see Decision 1.
- **Accepting a JSON number as a secret.** Rejected: see Decision 2. A secret
  that genuinely is all digits is written as a string — quoted in a file — and
  decodes exactly; from the environment, ADR 0097 bypasses the coercion for a
  secret field, so it arrives as written there too.
- **One file per version** in the file store. Rejected: a prune would be several
  unlinks that a crash can interrupt half-way, and numbering a new version would
  need a directory scan under the lock. One document per secret makes every
  change one atomic publication.
- **A precedence rule between `NAME` and `NAME_FILE`.** Rejected: whichever wins,
  a deployment that set both by mistake runs on the one the operator did not
  mean, silently.
- **Using a version's bytes directly as both AES and HMAC key.** Rejected for
  HKDF with two labels; the derivation costs a microsecond.
- **A goroutine-owning rotator** (`Start`/`Stop`). Rejected: `Run` on the
  caller's goroutine is joinable by construction and leaves supervision to the
  caller, which is where the SDK's other long-running loops leave it.

## Deferred

- **Vault, KMS, cloud secret managers** — each a `Store` implementation, and a
  connector (`third-party/`) rather than a mechanism. The port is frozen so one
  can be written against it today.
- **TLS certificate reload** from a secret — a consumer of this domain and of
  `net`, and its own decision.
- **Memory locking and guaranteed erasure** (`mlock`, zeroing on GC). Go's
  collector moves and copies memory and a string cannot be cleared; the domain
  clears the buffers it owns and promises nothing more.
- **Deleting a secret outright**, and a conditional `Put` that would make
  `RotateIfDue` atomic without a `Locker` — both future ADR 0039 siblings.
- **A file store on Windows** — waits on `vfs`'s own Windows backend.

## Verification

- `internal/core/secret`: every rendering (fmt verbs, a pointer, exported and
  unexported fields, slices, maps, JSON, text, `VersionValue`) is checked for
  the secret in four spellings — text, byte list, hex, base64; the JSON and text
  decoders, the two refusals and the dump-then-reload failure.
- `internal/service/secret`: one conformance table runs the numbering, refusal
  and copy contracts against the memory store, the file store and the sealed
  file store; the file store's modes, directory refusal, no-plaintext claim
  (every file in the directory, including temporaries, searched for the value,
  its base64 and hex, and the JSON field names), torn-read freedom under a
  concurrent writer, cross-instance writer serialisation, and every
  `RECORD_UNREADABLE` path; the environment store's mapping, `_FILE`
  convention and refusals; the keyring across two rotations and a prune; the
  rotator on a `ManualClock`, `Run` included, and through a `Locker`; sixteen
  concurrent first uses of one `KeyFile` path returning one key, with the file
  0600, the directory 0700 and no temporary left, every reshaped content
  refused unchanged and unrepeated, and a sealed store reopened under the key
  read back.
- `pkg/v1/secret`: `config.Load` into a `Value`, printed, dumped, logged through
  the SDK logger, and a keyring across rotations, through public names only.
- `pkg/v1/logger/slogbridge`: slog's text and JSON handlers and the bridge.

## References

- `internal/core/secret/CLAUDE.md`, `internal/service/secret/CLAUDE.md`,
  `pkg/v1/secret/README.md`
- The Docker secrets `_FILE` convention, as the official `postgres` and `mysql`
  images implement it (both variables set is an error there too)
- RFC 1123 §2.1 (the label grammar), RFC 5869 (HKDF)
