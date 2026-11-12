//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

var (
	// A ring whose connections are process descriptors can name a peer.
	_ peerer = (*uRing)(nil)
	// The backend sends a file's bytes from the kernel.
	_ fileSender = (*uRing)(nil)
)
