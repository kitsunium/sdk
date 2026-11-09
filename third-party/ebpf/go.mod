module github.com/kitsunium/sdk/third-party/ebpf

go 1.27.3

require (
	github.com/cilium/ebpf v0.22.0
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.48.0
)

replace github.com/kitsunium/sdk/internal => ../../internal
