//go:build linux

package loop

var (
	// epoll's connections are process descriptors, so it can name a peer.
	_ peerer = (*epollLoop)(nil)
	// The backend sends a file's bytes from the kernel.
	_ fileSender = (*epollLoop)(nil)
)
