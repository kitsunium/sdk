//go:build windows

// Package ipc — the private endpoint on Windows: a named pipe.
//
// A Unix socket there is a file in a directory, but Windows has no search
// permission to make a directory the gate, so the endpoint is a named pipe,
// the object Windows secures by itself:
//
//   - its DACL grants this process's account only, protected from
//     inheritance (SDDL "D:P(A;;GA;;;<SID>)"), so no other account can open
//     it — Everyone, Authenticated Users and Administrators included;
//   - PIPE_REJECT_REMOTE_CLIENTS refuses a client of another machine;
//   - the first instance is created with FILE_FLAG_FIRST_PIPE_INSTANCE: when
//     the name exists — another daemon, or a squatter that made it first —
//     the listen fails (IN_USE) rather than joining an instance somebody
//     else controls, and the next instance is created before a connection is
//     handed out, so the name never lapses while the listener lives;
//   - each end reads the other's account from its process token
//     (GetNamedPipeClientProcessId / GetNamedPipeServerProcessId, then
//     OpenProcessToken): a client refuses a server of another account before
//     it sends a byte (ENDPOINT_FOREIGN), and opens the pipe at
//     SECURITY_IDENTIFICATION, so the server can learn who it is but never
//     act as it.
//
// The pipe is named after Config.Path, so one configuration serves every
// platform: \\.\pipe\ipc-<16 hex digits of the path's SHA-256>-<base name>.
// Nothing is created in the path's directory.
//
// The handles are opened overlapped and handed to os.NewFile, which puts
// them on the runtime's poller: reads, writes and deadlines are the os
// package's. kernel32 and advapi32 are bound with syscall.NewLazyDLL, as the
// lock domain binds LockFileEx and GetNamedSecurityInfoW — golang.org/x/sys
// is banned SDK-wide (ADR 0018).
package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The flags and codes of winbase.h and winerror.h this file uses.
const (
	pipeAccessDuplex          uint32        = 0x00000003
	fileFlagFirstPipeInstance uint32        = 0x00080000
	pipeRejectRemoteClients   uint32        = 0x00000008 // with PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT, all 0
	pipeUnlimitedInstances    uint32        = 255
	pipeBufferSize            uint32        = 64 << 10
	securitySQOSPresent       uint32        = 0x00100000
	securityIdentification    uint32        = 0x00010000
	processQueryLimitedInfo   uint32        = 0x00001000
	sddlRevision1             uint32        = 1
	errorPipeBusy             syscall.Errno = 231
	errorNoData               syscall.Errno = 232
	errorPipeConnected        syscall.Errno = 535
	pipePrefix                string        = `\\.\pipe\ipc-`
	pipeBusyRetry             time.Duration = 10 * time.Millisecond
	pipeHashBytes             int           = 8
)

// The entry points the syscall package does not export, all in Windows since
// Vista.
var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	modadvapi32 = syscall.NewLazyDLL("advapi32.dll")

	procCreateNamedPipe   = modkernel32.NewProc("CreateNamedPipeW")
	procConnectNamedPipe  = modkernel32.NewProc("ConnectNamedPipe")
	procPipeClientProcess = modkernel32.NewProc("GetNamedPipeClientProcessId")
	procPipeServerProcess = modkernel32.NewProc("GetNamedPipeServerProcessId")
	procCreateEvent       = modkernel32.NewProc("CreateEventW")
	procOverlappedResult  = modkernel32.NewProc("GetOverlappedResult")
	procSDDLToDescriptor  = modadvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
)

// pipeAddr is a named pipe's address.
type pipeAddr string

// Network is the address's network, "pipe".
func (pipeAddr) Network() string { return "pipe" }

// String is the pipe's name.
func (a pipeAddr) String() string { return string(a) }

// pipeConn is a connected pipe instance: the os package's file, which reads,
// writes and keeps deadlines on the runtime's poller, plus the addresses a
// net.Conn has.
type pipeConn struct {
	*os.File
	addr pipeAddr
}

// LocalAddr is the pipe's name.
func (c *pipeConn) LocalAddr() net.Addr { return c.addr }

// RemoteAddr is the pipe's name: a pipe has no other address.
func (c *pipeConn) RemoteAddr() net.Addr { return c.addr }

// pipeAcceptor holds the instance the next client connects to. One Accept
// waits at a time (acceptMu); Close cancels the wait without that lock.
type pipeAcceptor struct {
	name pipeAddr
	sddl string

	acceptMu sync.Mutex

	mu      sync.Mutex
	pending syscall.Handle
	waiting bool
	closed  bool
}

// pipeName is the pipe that stands for the socket path.
func pipeName(path string) pipeAddr {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
	return pipeAddr(pipePrefix + hex.EncodeToString(sum[:pipeHashBytes]) + "-" + filepath.Base(path))
}

// accountOf is the account a process runs as, from its token; pid 0 is this
// process.
func accountOf(pid uint32) (string, error) {
	tok, err := tokenOf(pid)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := tok.Close(); cerr != nil {
			log.Printf("ipc: closing a process token: %v", cerr)
		}
	}()
	user, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String()
}

// tokenOf opens a process's token for reading; pid 0 is this process.
func tokenOf(pid uint32) (syscall.Token, error) {
	if pid == 0 {
		return syscall.OpenCurrentProcessToken()
	}
	proc, err := syscall.OpenProcess(processQueryLimitedInfo, false, pid)
	if err != nil {
		return 0, err
	}
	defer closeHandles(proc)
	var tok syscall.Token
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY, &tok); err != nil {
		return 0, err
	}
	return tok, nil
}

// pipeProcess is the process at the other end of h, by one of the two
// GetNamedPipe*ProcessId entry points' Call.
func pipeProcess(call func(a ...uintptr) (uintptr, uintptr, error), h syscall.Handle) (uint32, error) {
	var pid uint32
	if r, _, err := call(uintptr(h), uintptr(unsafe.Pointer(&pid))); r == 0 {
		return 0, err
	}
	return pid, nil
}

// listen creates the pipe's first instance, refusing a name that exists.
func listen(cfg *Config) (*Listener, error) {
	self, err := accountOf(0)
	if err != nil {
		return nil, errs.Wrap(ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path),
			errs.String("cause", "this process's account cannot be read: "+err.Error()))
	}
	a := &pipeAcceptor{name: pipeName(cfg.Path), sddl: "D:P(A;;GA;;;" + self + ")"}
	h, err := a.create(true)
	switch {
	case errors.Is(err, syscall.ERROR_ACCESS_DENIED), errors.Is(err, errorPipeBusy):
		return nil, errs.Wrap(InUse, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("pipe", string(a.name)))
	case err != nil:
		return nil, errs.Wrap(ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	a.pending = h
	return &Listener{cfg: *cfg, ln: a, self: -1, selfSID: self}, nil
}

// create makes one overlapped instance of the pipe under its DACL; first
// asks that it be the name's first.
func (a *pipeAcceptor) create(first bool) (syscall.Handle, error) {
	var sd uintptr
	sddl, err := syscall.UTF16PtrFromString(a.sddl)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	if r, _, err := procSDDLToDescriptor.Call(uintptr(unsafe.Pointer(sddl)), uintptr(sddlRevision1),
		uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return syscall.InvalidHandle, err
	}
	defer func() {
		if _, ferr := syscall.LocalFree(syscall.Handle(sd)); ferr != nil {
			log.Printf("ipc: freeing a security descriptor: %v", ferr)
		}
	}()
	sa := syscall.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, err := syscall.UTF16PtrFromString(string(a.name))
	if err != nil {
		return syscall.InvalidHandle, err
	}
	mode := pipeAccessDuplex | syscall.FILE_FLAG_OVERLAPPED
	if first {
		mode |= fileFlagFirstPipeInstance
	}
	r, _, err := procCreateNamedPipe.Call(uintptr(unsafe.Pointer(name)), uintptr(mode), uintptr(pipeRejectRemoteClients),
		uintptr(pipeUnlimitedInstances), uintptr(pipeBufferSize), uintptr(pipeBufferSize), 0, uintptr(unsafe.Pointer(&sa)))
	if syscall.Handle(r) == syscall.InvalidHandle {
		return syscall.InvalidHandle, err
	}
	return syscall.Handle(r), nil
}

// accept waits for a client on the pending instance, puts the next instance
// in its place, and returns the connection with the client's account. A
// client that came and went before the wait saw it is skipped.
func (a *pipeAcceptor) accept() (net.Conn, PeerValue, error) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	for {
		h, err := a.connectNext()
		switch {
		case errors.Is(err, errorNoData):
			continue
		case err != nil:
			return nil, PeerValue{}, err
		}
		return &pipeConn{File: os.NewFile(uintptr(h), string(a.name)), addr: a.name}, clientPeer(h), nil
	}
}

// connectNext waits for a client on the pending instance and puts the next
// one in its place; it returns the connected instance, or closes it and says
// why: net.ErrClosed once the listener closed, errorNoData for a client gone
// before the wait saw it.
func (a *pipeAcceptor) connectNext() (syscall.Handle, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return syscall.InvalidHandle, net.ErrClosed
	}
	h := a.pending
	a.waiting = true
	a.mu.Unlock()

	err := connectPipe(h)
	next, nextErr := a.create(false)

	a.mu.Lock()
	a.waiting = false
	closed := a.closed
	a.pending = next
	if closed || nextErr != nil {
		a.pending = syscall.InvalidHandle
	}
	a.mu.Unlock()

	switch {
	case closed:
		closeHandles(h, next)
		return syscall.InvalidHandle, net.ErrClosed
	case nextErr != nil:
		closeHandles(h, next)
		return syscall.InvalidHandle, nextErr
	case err != nil:
		closeHandles(h)
		return syscall.InvalidHandle, err
	}
	return h, nil
}

// connectPipe waits, without a timeout, for a client on h; Close ends the
// wait with ERROR_OPERATION_ABORTED.
func connectPipe(h syscall.Handle) error {
	r, _, err := procCreateEvent.Call(0, 1, 0, 0)
	if r == 0 {
		return err
	}
	ev := syscall.Handle(r)
	defer closeHandles(ev)
	ov := syscall.Overlapped{HEvent: ev}
	r, _, err = procConnectNamedPipe.Call(uintptr(h), uintptr(unsafe.Pointer(&ov)))
	switch {
	case r != 0, errors.Is(err, errorPipeConnected):
		return nil
	case !errors.Is(err, syscall.ERROR_IO_PENDING):
		return err
	}
	var n uint32
	if r, _, err := procOverlappedResult.Call(uintptr(h), uintptr(unsafe.Pointer(&ov)), uintptr(unsafe.Pointer(&n)), 1); r == 0 {
		return err
	}
	return nil
}

// clientPeer is the client of h: its process, and its account when its token
// can be read; unverified otherwise — the DACL admitted it.
func clientPeer(h syscall.Handle) PeerValue {
	p := PeerValue{UID: -1, GID: -1}
	pid, err := pipeProcess(procPipeClientProcess.Call, h)
	if err != nil {
		return p
	}
	p.PID = int(pid)
	if sid, err := accountOf(pid); err == nil {
		p.SID, p.Verified = sid, true
	}
	return p
}

// closeHandles closes handles this package no longer needs. A close that
// fails changes nothing — the handle is abandoned either way —, so it is
// logged, never returned in place of the verdict that caused it.
func closeHandles(hs ...syscall.Handle) {
	for _, h := range hs {
		if h == syscall.InvalidHandle {
			continue
		}
		if err := syscall.CloseHandle(h); err != nil {
			log.Printf("ipc: closing a pipe handle: %v", err)
		}
	}
}

// Close stops accepting: a waiting accept is cancelled and closes the
// instance itself, an idle one is closed here.
func (a *pipeAcceptor) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.waiting {
		return syscall.CancelIoEx(a.pending, nil)
	}
	closeHandles(a.pending)
	a.pending = syscall.InvalidHandle
	return nil
}

// Addr is the pipe's name.
func (a *pipeAcceptor) Addr() net.Addr { return a.name }

// admits is the Windows admission: this account, or a client whose account
// could not be read — the DACL, which grants this account only, admitted it.
func (l *Listener) admits(p PeerValue) error {
	if !p.Verified || p.SID == l.selfSID {
		return nil
	}
	return errs.Wrap(PeerRefused, errs.WrapParams{}, errs.String("sid", p.SID), errs.Int("pid", p.PID))
}

// dial opens the pipe that stands for cfg.Path at SECURITY_IDENTIFICATION,
// waiting while every instance is busy, and refuses a server that does not
// run as this account — or whose account cannot be read.
func dial(ctx context.Context, cfg *Config) (*Conn, error) {
	self, err := accountOf(0)
	if err != nil {
		return nil, errs.Wrap(DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path),
			errs.String("cause", "this process's account cannot be read: "+err.Error()))
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	name := pipeName(cfg.Path)
	h, err := openPipe(ctx, name)
	if err != nil {
		return nil, errs.Wrap(DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	pid, err := pipeProcess(procPipeServerProcess.Call, h)
	var sid string
	if err == nil {
		sid, err = accountOf(pid)
	}
	switch {
	case err != nil:
		closeHandles(h)
		return nil, errs.Wrap(DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path),
			errs.String("cause", "the listener's account cannot be read: "+err.Error()))
	case sid != self:
		closeHandles(h)
		return nil, errs.Wrap(EndpointForeign, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("sid", sid))
	}
	c := &pipeConn{File: os.NewFile(uintptr(h), string(name)), addr: name}
	return &Conn{Conn: c, Peer: PeerValue{UID: -1, GID: -1, PID: int(pid), SID: sid, Verified: true}}, nil
}

// openPipe opens the pipe overlapped, retrying while every instance is
// busy, until ctx ends.
func openPipe(ctx context.Context, name pipeAddr) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(string(name))
	if err != nil {
		return syscall.InvalidHandle, err
	}
	retry := time.NewTimer(pipeBusyRetry)
	defer retry.Stop()
	for {
		h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING,
			syscall.FILE_FLAG_OVERLAPPED|securitySQOSPresent|securityIdentification, 0)
		if err == nil || !errors.Is(err, errorPipeBusy) {
			return h, err
		}
		retry.Reset(pipeBusyRetry)
		select {
		case <-ctx.Done():
			return syscall.InvalidHandle, err
		case <-retry.C:
		}
	}
}
