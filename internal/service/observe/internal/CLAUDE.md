<!-- updated: 2026-10-03T05:20:00Z -->
# internal/service/observe/internal/

## Purpose

The observe family's private helpers. This directory holds no Go code: it is a
prefix, not a package. Go's `internal/` rule lets only a package under
`internal/service/observe` import what sits below it, so a helper placed here
is shared by the family's engines and reachable by nothing else — not by
another service domain, and not by `pkg/v1`.

## Members

| Package | What it is | Importers |
|---|---|---|
| `otlp/` | the OTLP machinery both signals speak, written once: the proto3-JSON scalars, the `common` and `resource` messages, the single-document marshal, the NDJSON stream and the OTLP/HTTP sender. It owns no code — each signal hands it a `SignalSpec` with its sentinels, its wording and its field names, so every error leaves under that signal's code (ADR 0048, ADR 0051) | `observe/metrics`, `observe/trace` |

ADR 0155 puts the emitter the two signals share inside their family, internal
(§1, the `observe` row): beside its importers, at the deepest directory that
holds them both, so the `internal/` rule says who may use it.

## Do NOT

- Put Go code in this directory.
- Add a helper only one member uses: it belongs in that member.
- Add a helper a member of another family needs: it is not this family's, and
  the `internal/` rule would refuse the import anyway.
