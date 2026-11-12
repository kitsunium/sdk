//go:build unix

package ipc

import (
	"errors"
	"log"
	"net"
	"os"
	"strconv"
	"syscall"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// listenBacklog is the backlog asked of listen(2): every kernel clamps it to
// its own maximum (somaxconn), which is what net.ListenUnix ends up with too.
const listenBacklog int = 1<<16 - 1

// bindSocket binds a Unix stream socket at path and gives the file p's group
// and mode, without listening yet: until listenBound, a connect(2) is refused
// (ECONNREFUSED), so no connection is made while the file still has the mode
// the umask gave it at bind — wider than p's, under a permissive umask. The
// returned file holds the socket; on failure nothing stays at path.
func bindSocket(path string, p posture) (*os.File, error) {
	//: the descriptor is marked close-on-exec under ForkLock, as the net
	//: package does, so no child forked meanwhile inherits it.
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, listenFailed(path, err.Error())
	}
	f := os.NewFile(uintptr(fd), path)
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		closeBestEffort(f, path)
		return nil, listenFailed(path, err.Error())
	}
	if err := setPosture(path, p); err != nil {
		abandon(f, path)
		return nil, err
	}
	return f, nil
}

// setPosture gives the bound socket file p's group, then p's mode. A group
// the file cannot take — this account is not a member, and is not root — is
// a refusal: left at its old group the socket would admit nobody the list
// names, and opened to everyone it would admit more than it names.
func setPosture(path string, p posture) error {
	if p.gid >= 0 {
		if err := os.Lchown(path, -1, p.gid); err != nil {
			return errs.Wrap(coreipc.ListenFailed, errs.WrapParams{}, errs.String("path", path),
				errs.String("cause", "the socket cannot be given the allowed group "+strconv.Itoa(p.gid)+
					" (this account must be a member of it): "+err.Error()))
		}
	}
	if err := os.Chmod(path, p.mode); err != nil {
		return listenFailed(path, err.Error())
	}
	return nil
}

// listenBound starts listening on the socket bindSocket bound at path and
// hands it to the net package, which removes path when the listener closes.
// f is closed either way; on failure nothing stays at path.
func listenBound(f *os.File, path string) (*net.UnixListener, error) {
	if err := listenOn(f); err != nil {
		abandon(f, path)
		return nil, listenFailed(path, err.Error())
	}
	ln, err := net.FileListener(f)
	//: FileListener holds a duplicate; this descriptor's work is done.
	closeBestEffort(f, path)
	if err != nil {
		removeBestEffort(path)
		return nil, listenFailed(path, err.Error())
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		closeBestEffort(ln, path)
		removeBestEffort(path)
		return nil, listenFailed(path, "not a unix listener")
	}
	//: the listener removes the file it created when it closes, and only then.
	ul.SetUnlinkOnClose(true)
	return ul, nil
}

// listenOn calls listen(2) on the socket f holds.
func listenOn(f *os.File) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var listenErr error
	if err := raw.Control(func(fd uintptr) { listenErr = syscall.Listen(int(fd), listenBacklog) }); err != nil {
		return err
	}
	return listenErr
}

// abandon closes a socket that will not listen and removes its file.
func abandon(f *os.File, path string) {
	closeBestEffort(f, path)
	removeBestEffort(path)
}

// removeBestEffort removes the socket file this listen bound and gives up on:
// the refusal already says why, and a file that cannot be removed is a
// leftover the next Listen clears.
func removeBestEffort(path string) {
	//: a remove that fails is worth a line, not a changed outcome.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		//: the path says which socket, the error what the kernel said.
		log.Printf("ipc: removing the abandoned socket %s: %v", path, err)
	}
}

// listenFailed is LISTEN_FAILED at path with its cause.
func listenFailed(path, cause string) error {
	return errs.Wrap(coreipc.ListenFailed, errs.WrapParams{}, errs.String("path", path), errs.String("cause", cause))
}
