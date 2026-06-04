// features.mjs — the CURATED feature catalog for the docs site.
//
// This is the single source of truth for the "What's new" banner and the
// Features catalog page. Unlike the old commit-derived feed, every entry here
// is a hand-authored, consumer-facing PUBLIC CAPABILITY — grouped by domain.
//
// PROVENANCE IS NOT STORED HERE. Each feature's "added" date is derived at
// build time from its `anchor` — the first commit that introduced that file
// (see scripts/lib/features.mjs::deriveProvenance). If an anchor mis-dates a
// feature (e.g. the file predates the capability), repoint `anchor` at a more
// precise file; do NOT add a manual date.
//
// Each entry:
//   id      — stable kebab-case slug, unique across the catalog.
//   domain  — one of the public domains (see DOMAIN_ORDER in lib/features.mjs).
//   title   — short capability name (what the consumer can DO).
//   blurb   — one sentence, consumer-facing.
//   anchor  — a single repo-relative file under pkg/v1/** whose first commit
//             dates the feature. MUST be a file (not a dir): provenance uses
//             `git log --follow --diff-filter=A`. Prefer a file unlikely to be
//             renamed.
//   links   — optional { adr?: "0003", pr?: 58 }.
//   breaking— optional true for a breaking capability.
//
// To add a feature: append a row, pick a stable anchor, run
// `node scripts/gen-features.mjs` to confirm it dates correctly.

/** @type {Array<{id:string,domain:string,title:string,blurb:string,anchor:string,links?:{adr?:string,pr?:number},breaking?:boolean}>} */
export default [
  // ── codec ───────────────────────────────────────────────────────────────
  {
    id: "codec-dispatch",
    domain: "codec",
    title: "Universal Marshal / Unmarshal",
    blurb:
      "One Marshal/Unmarshal pair reaches all 18 wire formats (JSON, CBOR, MsgPack, TLV, XML, YAML, …) via a single Format string — swap formats with a one-word change.",
    anchor: "pkg/v1/codec/codec.go",
    links: { adr: "0003" },
  },
  {
    id: "codec-streaming",
    domain: "codec",
    title: "Streaming encode / decode",
    blurb:
      "NewEncoder / NewDecoder give incremental, bounded-memory I/O for every codec that supports streaming.",
    anchor: "pkg/v1/codec/codec.go",
    links: { adr: "0003" },
  },
  {
    id: "codec-baseN",
    domain: "codec",
    title: "Base-N byte encodings",
    blurb:
      "Base16 / Base32 / Base64 (std + URL) / Hex / ASCII85 are first-class Format constants behind the same dispatch.",
    anchor: "pkg/v1/codec/codec.go",
    links: { adr: "0003" },
  },
  {
    id: "codec-compressed",
    domain: "codec",
    title: "Self-describing compression frame",
    blurb:
      "MarshalCompressed / UnmarshalCompressed wrap any format in a Gzip/Flate frame that decodes without the caller knowing the algorithm up front.",
    anchor: "pkg/v1/codec/compressed.go",
    links: { adr: "0014" },
  },

  // ── logger ──────────────────────────────────────────────────────────────
  {
    id: "logger-structured",
    domain: "logger",
    title: "Structured zero-alloc logger",
    blurb:
      "NewText / Default plus typed attribute constructors and Debug/Info/Warn/Error helpers — structured records on a zero-alloc hot path.",
    anchor: "pkg/v1/logger/logger.go",
  },
  {
    id: "logger-builder",
    domain: "logger",
    title: "Chainable zero-alloc builder",
    blurb:
      "Build(lg, lvl).Str(k, v).Send(ctx, msg) — a pooled, allocation-free fluent builder for the hottest log paths.",
    anchor: "pkg/v1/logger/builder.go",
  },
  {
    id: "logger-multisink",
    domain: "logger",
    title: "Named multi-writer fan-out",
    blurb:
      "DefaultMulti / NewMulti fan a record out to named writers (console, file, rotfile) from a config-driven registry.",
    anchor: "pkg/v1/logger/writer.go",
    links: { adr: "0012" },
  },
  {
    id: "logger-sink-topology",
    domain: "logger",
    title: "Custom sink topology",
    blurb:
      "NewWithSink + Multi / ConsoleStderr / ConsoleStdout / NewWriterSink compose an arbitrary sink tree behind the Logger.",
    anchor: "pkg/v1/logger/sink.go",
  },
  {
    id: "logger-fromconfig",
    domain: "logger",
    title: "Config-driven construction",
    blurb:
      "FromConfig builds a fully wired Logger from a config blob (any codec format) with zero Go glue.",
    anchor: "pkg/v1/logger/fromconfig.go",
    links: { adr: "0014" },
  },
  {
    id: "logger-writer-registry",
    domain: "logger",
    title: "Pluggable writer registry",
    blurb:
      "Blank-import pkg/v1/logger/writer to register the console / file / rotfile sink factories used by the config-driven path.",
    anchor: "pkg/v1/logger/writer/writer.go",
    links: { adr: "0015" },
  },
  {
    id: "logger-version",
    domain: "logger",
    title: "Build-time version stamping",
    blurb:
      'Version (ldflags injection point) + FrameworkVersion() stamp every record with the framework version, defaulting to "dev".',
    anchor: "pkg/v1/logger/version.go",
    links: { adr: "0007" },
  },

  // ── errs ────────────────────────────────────────────────────────────────
  {
    id: "errs-introspection",
    domain: "errs",
    title: "Typed error introspection",
    blurb:
      "Read-only accessors — CodeOf / ReasonOf / HasCode / PrefixMatcher, the Public/Private message split, and HTTP-status / exit-code mapping — for routing on SDK errors.",
    anchor: "pkg/v1/errs/accessors.go",
    links: { adr: "0005" },
  },

  // ── crypto ──────────────────────────────────────────────────────────────
  {
    id: "crypto-aead",
    domain: "crypto",
    title: "AEAD Seal / Open (hidden nonce)",
    blurb:
      "Seal / Open with an embedded, never-reused nonce and a redacting Key — AES-256-GCM by default, hard to misuse, non-oracle on failure.",
    anchor: "pkg/v1/crypto/crypto.go",
    links: { adr: "0013" },
  },
  {
    id: "crypto-stream",
    domain: "crypto",
    title: "Streaming AEAD",
    blurb:
      "SealStream / OpenStream encrypt large payloads in authenticated 64 KiB chunks with truncation resistance.",
    anchor: "pkg/v1/crypto/crypto.go",
    links: { adr: "0014" },
  },
  {
    id: "crypto-envelope",
    domain: "crypto",
    title: "Key envelope (wrap / unwrap)",
    blurb:
      'WrapKey / UnwrapKey seal a data key under a passphrase in a self-describing "$kenv$…" frozen format.',
    anchor: "pkg/v1/crypto/crypto.go",
    links: { adr: "0014" },
  },

  // ── hash ────────────────────────────────────────────────────────────────
  {
    id: "hash-digest",
    domain: "hash",
    title: "Content hashing",
    blurb:
      "Sum / SumHex / New across 5 stdlib algorithms (SHA-256/512, SHA3-256, CRC32C, FNV-1a) for cache keys, content IDs and dedup — one-shot or streaming.",
    anchor: "pkg/v1/hash/hash.go",
    links: { adr: "0013" },
  },
  {
    id: "hash-cas",
    domain: "hash",
    title: "Content-addressed I/O",
    blurb:
      "DigestWriter tees writes while hashing; VerifyingReader checks an expected digest on EOF — content addressing without a second pass.",
    anchor: "pkg/v1/hash/hash.go",
    links: { adr: "0013" },
  },

  // ── sign ────────────────────────────────────────────────────────────────
  {
    id: "sign-detached",
    domain: "sign",
    title: "Detached signatures",
    blurb:
      "GenerateKey / Sign / Verify with Ed25519 or ECDSA-P256 — constant-time verify, non-oracle (an invalid signature is (false, nil)).",
    anchor: "pkg/v1/sign/sign.go",
    links: { adr: "0013" },
  },

  // ── mac ─────────────────────────────────────────────────────────────────
  {
    id: "mac-tag",
    domain: "mac",
    title: "Message authentication",
    blurb:
      "Tag / Verify under a shared secret (HMAC-SHA256) with constant-time comparison for detached integrity + authenticity.",
    anchor: "pkg/v1/mac/mac.go",
    links: { adr: "0014" },
  },

  // ── kdf ─────────────────────────────────────────────────────────────────
  {
    id: "kdf-subkey",
    domain: "kdf",
    title: "Key derivation (HKDF)",
    blurb:
      "Subkey expands one strong secret into independent, purpose-bound subkeys (HKDF-SHA256) for key separation.",
    anchor: "pkg/v1/kdf/kdf.go",
    links: { adr: "0013" },
  },
  {
    id: "kdf-keytree",
    domain: "kdf",
    title: "Hierarchical key tree",
    blurb:
      "NewKeyTree + Child(...).DeriveKey() derive path-addressed subkeys for structured key hierarchies.",
    anchor: "pkg/v1/kdf/kdf.go",
    links: { adr: "0014" },
  },

  // ── agree ───────────────────────────────────────────────────────────────
  {
    id: "agree-ecdh",
    domain: "agree",
    title: "Key agreement (ECDH)",
    blurb:
      "GenerateKey / SharedKey (X25519) let two parties derive the same symmetric Key without transmitting it — the raw DH secret is HKDF'd, never returned.",
    anchor: "pkg/v1/agree/agree.go",
    links: { adr: "0014" },
  },

  // ── password ────────────────────────────────────────────────────────────
  {
    id: "password-hash",
    domain: "password",
    title: "Password hashing + verify",
    blurb:
      "Hash / Verify / NeedsRehash with deliberately-slow PBKDF2-SHA256 and a self-describing PHC string for transparent upgrades.",
    anchor: "pkg/v1/password/password.go",
    links: { adr: "0013" },
  },
];
