//go:build windows

package ipc

import coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"

// Every private connection on Windows passes files both ways (#936): the
// receiver duplicates each handle out of the sender's process.
var (
	_ coreipc.FileSender   = (*pipeConn)(nil)
	_ coreipc.FileReceiver = (*pipeConn)(nil)
)
