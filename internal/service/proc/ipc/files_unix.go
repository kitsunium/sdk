//go:build unix

package ipc

import (
	"net"
	"os"
	"syscall"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// fdBytes is the width of one descriptor in an SCM_RIGHTS message.
const fdBytes int = 4

// fileConn is a private connection that passes open files (#936): the Unix
// connection, and the peer the kernel named for it.
type fileConn struct {
	*net.UnixConn
	peer coreipc.PeerValue
}

// withFiles wraps a connection so Conn.Conn offers FileSender and FileReceiver.
func withFiles(c *net.UnixConn, peer coreipc.PeerValue) net.Conn {
	return &fileConn{UnixConn: c, peer: peer}
}

// SendFiles sends payload with files attached, to a named peer only.
func (c *fileConn) SendFiles(payload []byte, files ...*os.File) error {
	switch {
	case !c.peer.Verified:
		return fdPassRefused("the kernel did not name the peer", nil)
	case len(payload) == 0:
		return fdPassRefused("an empty payload carries no descriptor", nil)
	}
	fds := make([]int, 0, len(files))
	for _, f := range files {
		fds = append(fds, int(f.Fd()))
	}
	if _, _, err := c.WriteMsgUnix(payload, syscall.UnixRights(fds...), nil); err != nil {
		return fdPassRefused("the send failed", err)
	}
	return nil
}

// RecvFiles reads one message and at most max descriptors; the kernel closes
// those beyond max (MSG_CTRUNC), and the net package receives them
// close-on-exec.
func (c *fileConn) RecvFiles(buf []byte, max int) (n int, files []*os.File, err error) {
	if max <= 0 {
		return 0, nil, fdPassRefused("max is not positive", nil)
	}
	oob := make([]byte, syscall.CmsgSpace(max*fdBytes))
	n, oobn, flags, _, rerr := c.ReadMsgUnix(buf, oob)
	if rerr != nil {
		return n, nil, fdPassRefused("the receive failed", rerr)
	}
	messages, perr := syscall.ParseSocketControlMessage(oob[:oobn])
	if perr != nil {
		return n, nil, fdPassRefused("the control message did not parse", perr)
	}
	for i := range messages {
		fds, uerr := syscall.ParseUnixRights(&messages[i])
		if uerr != nil {
			continue
		}
		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "ipc-passed"))
		}
	}
	if flags&syscall.MSG_CTRUNC != 0 {
		return n, files, fdPassRefused("truncated", nil)
	}
	return n, files, nil
}
