// Package crypto — the AEAD port implemented by each concrete algorithm.
//
// Package crypto — the Agreement port: DH-style key agreement.
//
// Package crypto — the process-wide Agreement registry + GenerateAgreementKey / AgreementShared dispatch.
//
// Package crypto declares the authenticated-encryption port: the AEAD
// contract, the redacting Key value type, and the process-wide registry that
// maps an Algorithm (and its 1-byte wire id) to a registered AEAD. It is the
// peer of internal/core/data/codec — the registry resolves an Algorithm to an AEAD
// exactly as codec resolves a Format to a Codec.
//
// No algorithm bodies and no vendor types live here; concrete AEADs live under
// internal/service/crypto/<algo>/ (stdlib AES-GCM today) and third-party/
// x-crypto/* (XChaCha20-Poly1305 tomorrow) and self-register via a package-level
// var initialiser when imported — no init().
//
// Package crypto — range 0.2.4.* (ADR 0013 core/crypto block).
//
// Package crypto — the key-derivation port implemented by each KDF scheme.
//
// Package crypto — the process-wide Deriver registry + Subkey dispatch.
//
// Package crypto — declares the sentinels returned by the AEAD facade. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
// DuplicateRegistration is never returned: every registrar panics at boot with
// conflictText of it (see registry_generic.go and registry.go).
//
// Package crypto — the process-wide Hasher registry + Sum / SumHex dispatch.
//
// Package crypto — the Hasher port: fingerprint / content-addressing hashing.
//
// Package crypto — the opaque, redacting symmetric Key value type.
//
// Package crypto — the MAC port: keyed, detached message authentication.
//
// Package crypto — the process-wide MAC registry + MACTag / MACVerify dispatch.
//
// Package crypto — the password-hashing port implemented by each scheme.
//
// Package crypto — the process-wide PasswordHasher registry + HashPassword / VerifyPassword / NeedsRehash dispatch.
//
// Package crypto — holds the process-wide AEAD registry. Service- and
// third-party-level scheme packages register themselves via package-level var
// initialisers when imported (no init()), mirroring core/data/codec.
//
// Package crypto — the registrar every scheme registry shares (AEAD, Hasher,
// Signer, MAC, Deriver, Agreement, PasswordHasher, StreamSealer). The table
// under each one is the kernel's (kernel/plugin.Registry, ADR 0159); what this
// file adds is the crypto domain's refusal — the code, the reason and the
// fields a conflict panics with — written once instead of once per capability.
//
// Package crypto — the Seal / Open dispatch over the AEAD registry.
//
// Package crypto — the digital-signature port implemented by each scheme.
//
// Package crypto — the process-wide Signer registry + GenerateKey / Sign / Verify dispatch.
//
// Package crypto — the StreamSealer port: chunked authenticated encryption.
//
// Package crypto — the process-wide StreamSealer registry + SealStream / OpenStream dispatch.
package crypto
