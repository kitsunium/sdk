<!-- updated: 2026-11-08T12:00:00Z -->
# internal/core/proc/bpf/

## Purpose

The contract of the BPF programs a supervisor attaches to a control group
(ADR 0215, kitsunium/sdk#731 S6): the three policies as plain values, their
validation, the `Backend` port a vendor module implements and the
`Program` / `SKBProgram` ports an attachment satisfies, and the codes
`0.3.107.*`, declared here at the path of their engine,
`internal/service/proc/bpf` (ADR 0160). Standard library only: the backend is
`third-party/ebpf/cgroup`, over cilium/ebpf, and nothing here imports it. The
facade is `pkg/v1/proc/bpf`.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from `design/proc/bpf.yaml` |
| `values.go` | `Verdict` (`Deny`, `Allow`; the zero refused), `DeviceType`, `DeviceAccess`, `Family`, `Transport`; `DeviceRuleValue`, `DevicePolicyValue`, `PrefixRuleValue`, `SKBPolicyValue`, `PortRangeValue`, `BindRuleValue`, `BindPolicyValue`, `TrafficValue`, `CountersValue`; `AnyNumber`, `MaxDeviceRules`, `MaxBindRules`, `MaxPrefixRules` |
| `validate.go` | the three `Validate` methods and `CheckCgroup` — `POLICY_INVALID` naming the rule (`-1` for the policy) and the field |
| `gen_ports.go` | GENERATED — `Program`, `SKBProgram`, `Backend` |
| `gen_codes.go` | GENERATED — `0.3.107.1`–`6` and their sentinels |

## Conventions

- **A verdict is never the zero** (ADR 0031): `Default` and every rule's
  `Verdict` are `Allow` or `Deny`, so `DevicePolicyValue{}` is refused rather
  than read as deny-all, and `SKBPolicyValue{}` rather than as drop-all. The
  other enumerations start at 1 for the same reason.
- **Bodies here are pure value methods** — `Validate`, `CheckCgroup`; the
  registry and the dispatch are the engine's.
- **`UNSUPPORTED_PLATFORM` is `core/proc`'s**, re-exported by the facade; it is
  the backend's answer off Linux amd64 and arm64.

## Do NOT

- Import cilium/ebpf, `golang.org/x/sys` or anything outside the standard
  library and the kernel: the quarantine is the reason the backend is a
  vendor module.
- Widen `Backend`, `Program` or `SKBProgram` (ADR 0039): a later capability is
  a sibling interface.
- Declare a code outside `design/proc/bpf.yaml`.

## Verification

```sh
cd internal && go test -race ./core/proc/bpf/
```
