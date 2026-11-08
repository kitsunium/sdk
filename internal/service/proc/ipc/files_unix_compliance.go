//go:build unix

package ipc

import coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"

// Every private connection on Unix passes files both ways (#936).
var (
	_ coreipc.FileSender   = (*fileConn)(nil)
	_ coreipc.FileReceiver = (*fileConn)(nil)
)
