# cgroup — BPF programs on a control group, over cilium/ebpf

`import _ "github.com/kitsunium/sdk/third-party/ebpf/cgroup"` (module `github.com/kitsunium/sdk/third-party/ebpf`)

The backend of `pkg/v1/proc/bpf`: importing it registers the loader, and
`bpf.AttachDevice`, `bpf.AttachSKB` and `bpf.AttachBind` then attach
`cgroup_device`, `cgroup_skb` and `cgroup_sock_addr` programs built in Go with
cilium/ebpf's `asm` — no C, no clang, no object file.

```go
import (
	"github.com/kitsunium/sdk/pkg/v1/proc/bpf"
	_ "github.com/kitsunium/sdk/third-party/ebpf/cgroup"
)

p, err := bpf.AttachBind(tree.Path(), bpf.BindPolicy{Default: bpf.Allow, Rules: []bpf.BindRule{
	{Family: bpf.FamilyAny, Transport: bpf.TransportAny, Ports: bpf.PortRange{First: 1, Last: 1023}, Verdict: bpf.Deny},
}})
```

Linux on amd64 and arm64, with CAP_BPF and CAP_NET_ADMIN (or root);
`UNSUPPORTED_PLATFORM` elsewhere, `LOAD_REFUSED` without the privilege. Each
program is attached with `BPF_F_ALLOW_MULTI` and stays until `Close` or the
group's removal. See the package comment and `CLAUDE.md`.
