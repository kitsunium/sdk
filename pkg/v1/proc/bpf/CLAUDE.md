<!-- updated: 2026-11-08T12:00:00Z -->
# pkg/v1/proc/bpf/

## Purpose

Public facade over `internal/service/proc/bpf`, its contract `internal/core/proc/bpf` (ADR 0215, kitsunium/sdk#731 S6):
the BPF programs a supervisor attaches to a control group — a device policy
(`cgroup_device`), an IP policy that counts its traffic (`cgroup_skb`), a bind
policy (`cgroup_sock_addr`). Every symbol is an alias, a re-export or a
forwarder kit writes from `design/proc/bpf.yaml` (`gen_facade.go`); nothing
here is hand-written but the package comment's source.

The backend is not here and not in the core: it is the vendor module
`third-party/ebpf` (cilium/ebpf), linked by importing
`github.com/kitsunium/sdk/third-party/ebpf/cgroup`, even blank. This package
links nothing of cilium/ebpf — `pkg/v1/proc`'s `go.mod` does not even require
it — and answers `NotLinked` in a program that did not import the vendor.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `DevicePolicy`, `DeviceRule`, `SKBPolicy`, `PrefixRule`, `BindPolicy`, `BindRule`, `PortRange`, `Traffic`, `Counters` | type alias | the core's `…Value` structs |
| `Verdict` (`Deny`, `Allow`), `DeviceType`, `DeviceAccess`, `Family`, `Transport` | type alias, const | every zero is refused — a policy says what it decides (ADR 0031) |
| `AnyNumber`, `MaxDeviceRules`, `MaxBindRules`, `MaxPrefixRules` | const | the wildcard and the bounds |
| `Program`, `SKBProgram` | type alias, port | `Close` detaches; `Counters` reads the IP policy's two counts |
| `Probe`, `AttachDevice`, `AttachSKB`, `AttachBind` | func | forward to the engine `internal/service/proc/bpf`: the policy, then the path, then the backend |
| `Code*`, `PolicyInvalid`, `NotLinked`, `LoadRefused`, `AttachFailed`, `ProgramFailed`, `UnsupportedPlatform` | const, var | `0.3.107.*` and `core/proc`'s `UNSUPPORTED_PLATFORM` |

## Do NOT

- Import `third-party/ebpf` here: a consumer opts in by its own import, and a
  family module that required cilium/ebpf would hand it to every consumer of
  `pkg/v1/proc` (ADR 0157).
- Hand-edit `gen_facade.go`, `doc.go` or `README.md`: the design writes them.

## Verification

```sh
cd pkg/v1/proc && go test -race ./bpf/
```
