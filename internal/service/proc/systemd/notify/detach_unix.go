//go:build linux || freebsd

package notify

import (
	"net"
	"os"
	"path/filepath"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

const (
	// tempDirPrefix opens the name of the private directory Listen binds in.
	tempDirPrefix string = "sdnotify-"
	// socketName is the socket's name in that directory.
	socketName string = "notify"
)

// Detach readies l — Listen's or ListenFor's — to be handed to another image
// of this process (kitsunium/sdk#738): it returns a new close-on-exec
// descriptor on the socket, and l's Close no longer removes the socket's
// path nor its directory, which Adopt's listener removes in turn. Any other
// Listener is ListenFailed.
func Detach(l coreproc.Listener) (f *os.File, err error) {
	ln, ok := l.(*listener)
	//: not a listener of this package.
	if !ok || ln == nil {
		//: LISTEN_FAILED.
		return nil, wrapListen(os.ErrInvalid, "")
	}
	f, err = ln.conn.File()
	//: no descriptor.
	if err != nil {
		//: LISTEN_FAILED naming the socket.
		return nil, wrapListen(err, ln.path)
	}
	ln.path, ln.tempDir = "", ""
	//: the descriptor, close-on-exec.
	return f, nil
}

// Adopt is the listener over a socket Detach handed over, bound at
// socketPath: the kernel stamps every datagram with its sender again, and
// Close removes the path — and the private directory Listen made, named
// sdnotify-… — as Listen's listener does. f is the caller's; a descriptor
// that is no datagram socket of the unix family is ListenFailed.
func Adopt(f *os.File, socketPath string) (l coreproc.Listener, err error) {
	c, err := net.FileConn(f)
	//: no socket.
	if err != nil {
		//: LISTEN_FAILED naming the path.
		return nil, wrapListen(err, socketPath)
	}
	conn, ok := c.(*net.UnixConn)
	//: a socket of another kind.
	if !ok || conn.LocalAddr().Network() != "unixgram" {
		swallowErr(c.Close())
		//: LISTEN_FAILED naming the path.
		return nil, wrapListen(os.ErrInvalid, socketPath)
	}
	//: the credentials, stamped again on this descriptor's socket.
	if err = enableCreds(conn); err != nil {
		swallowErr(conn.Close())
		//: the option's refusal.
		return nil, err
	}
	ln := &listener{conn: conn, path: socketPath}
	dir := filepath.Dir(socketPath)
	//: the private directory Listen made, its socket at the name Listen
	//: gives it: removed with the socket.
	if strings.HasPrefix(filepath.Base(dir), tempDirPrefix) && filepath.Base(socketPath) == socketName {
		ln.tempDir = dir
	}
	//: the listener.
	return ln, nil
}
