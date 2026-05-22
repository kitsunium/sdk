<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/codec/

## Purpose

Declares the domain contract every wire-format codec in the SDK satisfies (`Codec`), the optional streaming extension (`StreamingCodec`), the optional append-into-buffer extension (`Appender`), and the **process-wide registry** that maps `Format` / MIME / file extension to the registered codec. ADR 0003.

No format-specific knowledge lives here — concrete codecs live under `internal/service/codec/<format>/` and self-register at package import. `pkg/v1/codec` blank-imports all ten of them and re-exports `Marshal` / `Unmarshal` / `Lookup*` against this package.

## Contents

| File | Surface |
|---|---|
| `codec_interface.go` | `Codec` (`Name` / `MIMETypes` / `Extensions` / `Marshal` / `Unmarshal`), `StreamingCodec` (adds `NewEncoder` / `NewDecoder`), `Encoder` (`Encode` / `Close`), `Decoder` (`Decode` / `More`) |
| `appender.go` | `Appender` extension (`Append(dst, v) ([]byte, error)`) — hot-path zero-copy encode into a caller-owned buffer |
| `format.go` | `Format` typed string + `Known()` / `String()` |
| `registry.go` | Package-level `sync.Map` registry + `Register` / `Lookup` / `LookupMIME` / `LookupExt` / `Available`. All constructors marked `// IFACE-PLUGIN:` |
| `codes.go` | `CodeDuplicateRegistration` (0.2.2.1) — emitted via `panic` at boot; 0.2.2.2-4 reserved for Marshal/Unmarshal sentinels |

## Conventions

- **`sync.Map` not `sync.RWMutex`+map** — codecs Register exactly once at import; everything else is read-many. Documented at the top of `registry.go`.
- **Aliases are lowercased** before indexing; MIME parameters (`; charset=utf-8`) are stripped via `mime.ParseMediaType` with a manual fallback when the header is malformed.
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
