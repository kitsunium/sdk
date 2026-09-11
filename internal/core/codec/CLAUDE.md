<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/codec/

## Purpose

Declares the domain contract every wire-format codec in the SDK satisfies (`Codec`), the optional streaming extension (`StreamingCodec`), the optional append-into-buffer extension (`Appender`), and the **process-wide registry** that maps `Format` / MIME / file extension to the registered codec. ADR 0003.

No format-specific knowledge lives here — concrete codecs live under `internal/service/codec/<format>/` and self-register at package import. `pkg/v1/codec` blank-imports all sixteen of them (24 Format names) and re-exports `Marshal` / `Unmarshal` / `Lookup*` against this package.

## Contents

| File | Surface |
|---|---|
| `codec_interface.go` | `Codec` (`Name` / `MIMETypes` / `Extensions` / `Marshal` / `Unmarshal`), `StreamingCodec` (adds `NewEncoder` / `NewDecoder`), `Encoder` (`Encode` / `Close`), `Decoder` (`Decode` / `More`) |
| `appender.go` | `Appender` extension (`Append(dst, v) ([]byte, error)`) — hot-path zero-copy encode into a caller-owned buffer |
| `format.go` | `Format` typed string + `Known()` / `String()` |
| `registry.go` | Package-level `snapshot.Value`-backed registry (copy-on-write, ADR 0011) + `Register` / `Lookup` / `LookupMIME` / `LookupExt` / `Available`. All constructors marked `// IFACE-PLUGIN:` |
| `codes.go` | `CodeDuplicateRegistration` (0.2.2.1) — emitted via `panic` at boot; 0.2.2.2-4 reserved for Marshal/Unmarshal sentinels |

## Conventions

- **`snapshot.Value[map[K]V]` not `sync.Map`** — codecs Register exactly once at import; everything else is read-many. A frozen-after-init map read via `snapshot.Value.Load` (one `atomic.Pointer` load) is ~30% faster than `sync.Map.Load` + the interface-to-Codec type assertion (microbench: 9.1 ns vs 12.8 ns). The copy-on-write mechanism lives in `internal/kernel/snapshot` (ADR 0011); `registry.go` keeps only the domain clone logic (`cloneFormatMap` / `cloneAliasMap`). `Register` publishes via `Value.Update`, which serialises writers on a mutex — no hand-rolled CAS loop — while `Lookup*` stay lock-free via `Value.Load`.
- **Aliases are normalised symmetrically at registration and lookup.** Extensions are lowercased; MIME types go through `normalizeMIME` (strip parameters via `mime.ParseMediaType`, lowercase, trim; manual `;`-cut fallback on a malformed header) at BOTH `Register` and `LookupMIME` time, so a registered MIME is always reachable. A parameter-only alias therefore normalises to its bare media type and collides loudly rather than hiding behind it — see issue #36 (the former `base64url` `application/base64;url=true` alias was removed for exactly this reason).
- **Idempotent re-registration** of the same `Format` under the same alias is accepted; distinct codecs claiming the same name/MIME/ext **panic at boot** with the dotted-quad code in the message.
- **`Format("")` is reserved** as the invalid zero value — `Known()` returns false; `Lookup` of empty `Format` always misses.
- **`Appender` is optional**; consumers detect it with a type assertion and fall back to `Marshal` + copy. Hot paths (logger record formatting, NDJSON streams) prefer `Appender` when present.

## Do NOT

- Add a `MustRegister` variant — `Register` already panics on duplicate; a second entry-point hides the boot failure.
- Add deregistration / replacement APIs — the registry is append-only by contract.
- Import a service-layer codec from here. Service packages register themselves on import; core stays unaware of which formats exist.
- Inline format-specific logic (compaction, indent, …) into the interface — those belong on the concrete codec's constructor.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/codec:codec_test

# Fallback
cd internal/core && GOWORK=off go test -race -cover ./codec/...
# expected: registry concurrency + duplicate-detection + MIME-param stripping +
# Appender detection covered; ≥95% line coverage.
```

The audit (`bazel test //internal/kernel/errs:errs_test`, also run by `make test`) verifies `CodeDuplicateRegistration` stays in the `0.2.2.*` block with reason `DUPLICATE_REGISTRATION`.
