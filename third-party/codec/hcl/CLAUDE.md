# third-party/codec/hcl/

## Purpose

HCL codec — wraps HashiCorp HCL v2 behind the universal `core/codec.Codec`
dispatch. **Lives under `third-party/` (root module), NOT
`internal/service/codec`**, because `hashicorp/hcl/v2` pulls `go-cty` and a
heavier dep graph that, added to `internal/service`, would **introduce**
`golang.org/x/sys` there (measured: `v0.20.0`, pulled by `x/tools`) — a module
that is **banned SDK-wide**, which is exactly why `proc`'s syscall code is
written against raw stdlib `syscall`. Quarantining it in the root module keeps
the dep-light service module untouched (ADR 0022, with its mechanism corrected
by ADR 0034 — nothing is *downgraded*; mirrors the ADR 0012 writer-quarantine
policy). **Opt-in**: blank-import this package to
register the `"hcl"` Format — `pkg/v1/codec` does NOT pull it, so the public
module stays dep-light.

## Surface

| Aspect | Value |
|---|---|
| Format name | `"hcl"` |
| MIME types | `application/hcl` |
| Extensions | `.hcl` |
| Streaming | **no** |
| Appender | yes (`Marshal` + `append`) |
| Code range | `0.3.37.*` (ADR 0022) |

## Conventions

- **Top-level must be a struct.** HCL is decode-oriented; symmetric marshal is
  struct-only via `gohcl.EncodeIntoBody`, so `Marshal` requires a struct (or
  `*struct`) with `hcl:"…"` field tags. A non-struct returns `HCL_MARSHAL_FAILED`
  (checked by reflection before calling the library); a gohcl panic on an
  unsupported field type is **recovered** into the same sentinel.
- **Unmarshal** uses `hclsimple.Decode` with a nil `EvalContext` (no variables
  or functions) into a pointer to a struct/map.
- **Hard cap on Unmarshal**: 10 MiB (`maxHCLBytes`, CWE-400).
- **Errors** carry the dotted-quad codes via `errs.Wrap`; no `fmt.Errorf`.

## Error codes (range `0.3.37.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.37.1` | `MarshalFailed` | non-struct top level, or gohcl encode panic |
| `0.3.37.2` | `UnmarshalFailed` | `hclsimple.Decode` diagnostics (syntax/schema) |
| `0.3.37.3` | `SizeExceeded` | `len(data)` exceeds `maxHCLBytes` (10 MiB) |

## Do NOT

- Add HCL to `pkg/v1/codec`'s blank imports — it would pull `go-cty` into the
  dep-light public module. HCL is opt-in by design.
- Marshal a non-struct and expect success.
- Use `fmt.Errorf`/`errors.New`; wrap through `errs.Wrap`.

## Verification

```
bazel test --config=race //third-party/codec/hcl:hcl_test
```
