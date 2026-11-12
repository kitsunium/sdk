//go:build windows

package ipc

import (
	"encoding/binary"
	"io"
	"os"
	"syscall"
	"unsafe"
)

// The access right, the DuplicateHandle options and the frame a Windows pipe
// passes files with (#936).
const (
	// processDupHandle is PROCESS_DUP_HANDLE: the right to duplicate handles
	// out of a process, held on the peer from the connection on.
	processDupHandle uint32 = 0x0040
	// duplicateCloseSource and duplicateSameAccess are DuplicateHandle's
	// DUPLICATE_CLOSE_SOURCE and DUPLICATE_SAME_ACCESS.
	duplicateCloseSource uint32 = 0x00000001
	duplicateSameAccess  uint32 = 0x00000002
	// fileFrameMagic opens a frame of passed files: "SDK2", little-endian —
	// the second layout, each entry tagged with its kind, the only one
	// written.
	fileFrameMagic uint32 = 0x324B4453
	// fileFrameMagicV1 opens the first layout, "SDKF", which v0.60.0 (#1316)
	// writes: untagged handle values, no socket. It is still read, so a
	// supervisor and a child one version apart pass files during an upgrade
	// (ADR 0148, amendment 2026-11-11).
	fileFrameMagicV1 uint32 = 0x464B4453
	// fileFrameHeader is the frame's head: the magic, the entry count and
	// the payload's length, three little-endian uint32.
	fileFrameHeader int = 12
	// entryKindBytes is an entry's kind, a little-endian uint32.
	entryKindBytes int = 4
	// handleBytes is one handle value in the frame, a little-endian uint64.
	handleBytes int = 8
	// maxPassedFiles bounds one frame, as Linux's SCM_MAX_FD bounds one
	// SCM_RIGHTS message.
	maxPassedFiles int = 253
	// soType is SO_TYPE (winsock2.h): getsockopt answers it for a socket
	// alone, which tells a socket's handle from any other.
	soType int32 = 0x1008
	// fromProtocolInfo is FROM_PROTOCOL_INFO, -1 with every bit set: WSASocketW
	// reads the address family, type and protocol from the WSAPROTOCOL_INFOW
	// it is given.
	fromProtocolInfo uintptr = ^uintptr(0)
	// wsaFlagOverlapped and wsaFlagNoHandleInherit are WSASocketW's
	// WSA_FLAG_OVERLAPPED — the net package's sockets are overlapped — and
	// WSA_FLAG_NO_HANDLE_INHERIT.
	wsaFlagOverlapped      uint32 = 0x01
	wsaFlagNoHandleInherit uint32 = 0x80
)

// The kinds of a frame's entries.
const (
	// entryHandle is a handle in the sender's table, taken out with
	// DuplicateHandle: a handle value follows.
	entryHandle uint32 = 1
	// entrySocket is a socket WSADuplicateSocketW prepared for the receiver's
	// process: a WSAPROTOCOL_INFOW follows.
	entrySocket uint32 = 2
)

// protocolInfoBytes is the size of a WSAPROTOCOL_INFOW in the frame.
const protocolInfoBytes int = int(unsafe.Sizeof(syscall.WSAProtocolInfo{}))

// Winsock's sharing entry points, which the syscall package does not export;
// bound lazily, as the kernel32 ones are.
var (
	modWS2                  = syscall.NewLazyDLL("ws2_32.dll")
	procWSADuplicateSocketW = modWS2.NewProc("WSADuplicateSocketW")
	procWSASocketW          = modWS2.NewProc("WSASocketW")
)

// passedEntry is one file of a frame: a handle duplicated into the sender's
// own table, or a socket prepared for the receiver's process.
type passedEntry struct {
	// kind is entryHandle or entrySocket.
	kind uint32
	// handle is the private duplicate of an entryHandle.
	handle syscall.Handle
	// info is an entrySocket's WSAPROTOCOL_INFOW.
	info syscall.WSAProtocolInfo
}

// SendFiles sends payload with files attached, to a named peer only. A
// pipe carries bytes, not handles: each file is duplicated into this
// process's own table, private and not inheritable, and the frame — the
// magic, the count, the payload's length, the entries, the payload — tells
// the receiver which to take; RecvFiles duplicates each out of this process
// and closes it here (DUPLICATE_CLOSE_SOURCE). The receiver thus never wraps
// a number it was told: it takes what this process holds. A socket is not a
// handle DuplicateHandle may copy: WSADuplicateSocketW prepares it for the
// peer's process — held open since the connection, so its pid names it —
// and the frame carries the WSAPROTOCOL_INFOW, from which RecvFiles makes
// its own socket with WSASocketW, as the net package dups a socket within
// one process.
func (c *pipeConn) SendFiles(payload []byte, files ...*os.File) error {
	switch {
	case !c.peer.Verified:
		return fdPassRefused("the kernel did not name the peer", nil)
	case len(payload) == 0:
		return fdPassRefused("an empty payload carries no descriptor", nil)
	case len(files) > maxPassedFiles:
		return fdPassRefused("more files than one frame carries", nil)
	}
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return fdPassRefused("this process has no handle", err)
	}
	entries := make([]passedEntry, 0, len(files))
	for _, f := range files {
		entry, perr := c.prepare(self, f)
		if perr != nil {
			releaseEntries(entries)
			return perr
		}
		entries = append(entries, entry)
	}
	n, err := c.File.Write(fileFrame(entries, payload))
	if err != nil {
		// Nothing written: the receiver cannot take the duplicates, so they
		// are closed here. Part of the frame written: the receiver may take
		// them, and a second close would reach a handle this process reused;
		// they are left to it.
		if n == 0 {
			releaseEntries(entries)
		}
		return fdPassRefused("the send failed", err)
	}
	return nil
}

// prepare makes one file's entry: a socket prepared for the peer's process,
// or any other handle duplicated into this one's.
func (c *pipeConn) prepare(self syscall.Handle, f *os.File) (passedEntry, error) {
	h := syscall.Handle(f.Fd())
	if !isSocket(h) {
		var dup syscall.Handle
		if err := syscall.DuplicateHandle(self, h, self, &dup, 0, false, duplicateSameAccess); err != nil {
			return passedEntry{}, fdPassRefused("a file could not be duplicated", err)
		}
		return passedEntry{kind: entryHandle, handle: dup}, nil
	}
	//: the pid names the peer only while its process is held open.
	if c.proc == syscall.InvalidHandle {
		return passedEntry{}, fdPassRefused("the peer's process cannot be opened", nil)
	}
	entry := passedEntry{kind: entrySocket}
	if err := wsaDuplicateSocket(h, uint32(c.peer.PID), &entry.info); err != nil {
		return passedEntry{}, fdPassRefused("a socket could not be shared with the peer", err)
	}
	return entry, nil
}

// releaseEntries closes the duplicates of a frame that was not sent. A socket
// prepared for the peer is not this process's to close: the peer's process
// holds it until it exits.
func releaseEntries(entries []passedEntry) {
	for _, e := range entries {
		if e.kind == entryHandle {
			closeHandles(e.handle)
		}
	}
}

// isSocket reports whether h is a socket: getsockopt(SO_TYPE) answers for a
// socket alone, WSAENOTSOCK for any other handle.
func isSocket(h syscall.Handle) bool {
	var typ int32
	return syscall.Getsockopt(h, syscall.SOL_SOCKET, soType, (*byte)(unsafe.Pointer(&typ)), new(int32(unsafe.Sizeof(typ)))) == nil
}

// wsaDuplicateSocket is WSADuplicateSocketW: info receives what a process
// named pid needs to make its own socket of s.
func wsaDuplicateSocket(s syscall.Handle, pid uint32, info *syscall.WSAProtocolInfo) error {
	r1, _, errno := procWSADuplicateSocketW.Call(uintptr(s), uintptr(pid), uintptr(unsafe.Pointer(info)))
	if r1 != 0 {
		return errno
	}
	return nil
}

// wsaSocketFrom is WSASocketW(FROM_PROTOCOL_INFO): the socket info prepared
// for this process, overlapped as the net package's are and not inheritable.
func wsaSocketFrom(info *syscall.WSAProtocolInfo) (syscall.Handle, error) {
	r1, _, errno := procWSASocketW.Call(fromProtocolInfo, fromProtocolInfo, fromProtocolInfo,
		uintptr(unsafe.Pointer(info)), 0, uintptr(wsaFlagOverlapped|wsaFlagNoHandleInherit))
	if syscall.Handle(r1) == syscall.InvalidHandle {
		return syscall.InvalidHandle, errno
	}
	return syscall.Handle(r1), nil
}

// fileFrame lays out the frame SendFiles writes.
func fileFrame(entries []passedEntry, payload []byte) []byte {
	frame := make([]byte, fileFrameHeader, fileFrameHeader+len(entries)*(entryKindBytes+protocolInfoBytes)+len(payload))
	binary.LittleEndian.PutUint32(frame[0:], fileFrameMagic)
	binary.LittleEndian.PutUint32(frame[4:], uint32(len(entries)))
	binary.LittleEndian.PutUint32(frame[8:], uint32(len(payload)))
	for i := range entries {
		frame = binary.LittleEndian.AppendUint32(frame, entries[i].kind)
		if entries[i].kind == entryHandle {
			frame = binary.LittleEndian.AppendUint64(frame, uint64(entries[i].handle))
			continue
		}
		frame = append(frame, unsafe.Slice((*byte)(unsafe.Pointer(&entries[i].info)), protocolInfoBytes)...)
	}
	return append(frame, payload...)
}

// RecvFiles reads one frame SendFiles wrote: at most len(buf) bytes of its
// payload into buf — the rest stays in the stream, as on a Unix socket — and
// at most max of its files, each duplicated out of the sender's process,
// not inheritable, and closed there, or, for a socket, made here from the
// sender's WSAPROTOCOL_INFOW; those beyond max are closed there — a socket,
// already this process's, here — and FDPassRefused "truncated" says so,
// beside the files that fit. The stream must be at a frame: on Windows a
// file passes in band.
func (c *pipeConn) RecvFiles(buf []byte, max int) (n int, files []*os.File, err error) {
	if max <= 0 {
		return 0, nil, fdPassRefused("max is not positive", nil)
	}
	var head [fileFrameHeader]byte
	if _, err := io.ReadFull(c.File, head[:]); err != nil {
		return 0, nil, fdPassRefused("the receive failed", err)
	}
	count := int(binary.LittleEndian.Uint32(head[4:]))
	size := int(binary.LittleEndian.Uint32(head[8:]))
	magic := binary.LittleEndian.Uint32(head[0:])
	switch {
	case magic != fileFrameMagic && magic != fileFrameMagicV1:
		return 0, nil, fdPassRefused("the stream is not at a frame of files", nil)
	case count > maxPassedFiles:
		return 0, nil, fdPassRefused("more files than one frame carries", nil)
	}
	entries, err := c.readEntries(count, magic == fileFrameMagicV1)
	if err != nil {
		return 0, nil, err
	}
	n, rerr := io.ReadFull(c.File, buf[:min(size, len(buf))])
	files, perr := c.take(entries, max)
	switch {
	case rerr != nil:
		closeFiles(files)
		return n, nil, fdPassRefused("the receive failed", rerr)
	case perr != nil:
		return n, files, perr
	case count > max:
		return n, files, fdPassRefused("truncated", nil)
	}
	return n, files, nil
}

// readEntries reads a frame's count entries: tagged in the current layout,
// bare handle values in the first (v1). A kind it does not know ends the
// frame: what follows cannot be read.
func (c *pipeConn) readEntries(count int, v1 bool) ([]passedEntry, error) {
	entries := make([]passedEntry, count)
	var word [handleBytes]byte
	for i := range entries {
		entries[i].kind = entryHandle
		if !v1 {
			if _, err := io.ReadFull(c.File, word[:entryKindBytes]); err != nil {
				return nil, fdPassRefused("the receive failed", err)
			}
			entries[i].kind = binary.LittleEndian.Uint32(word[:entryKindBytes])
		}
		var err error
		switch entries[i].kind {
		case entryHandle:
			_, err = io.ReadFull(c.File, word[:])
			entries[i].handle = syscall.Handle(binary.LittleEndian.Uint64(word[:]))
		case entrySocket:
			_, err = io.ReadFull(c.File, unsafe.Slice((*byte)(unsafe.Pointer(&entries[i].info)), protocolInfoBytes))
		default:
			return nil, fdPassRefused("an entry of a kind this image does not know", nil)
		}
		if err != nil {
			return nil, fdPassRefused("the receive failed", err)
		}
	}
	return entries, nil
}

// take makes the first max entries this process's files and lets go of the
// others: a handle is duplicated out of the peer's process and closed there,
// a socket made here from its protocol info, and closed here when beyond
// max. Without a handle on the peer's process — an unnamed peer, or one
// this account may not open — no handle can be taken, and the sender keeps
// what it sent.
func (c *pipeConn) take(entries []passedEntry, max int) ([]*os.File, error) {
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return nil, fdPassRefused("this process has no handle", err)
	}
	var files []*os.File
	var failure error
	for i := range entries {
		keep := len(files) < max && failure == nil
		f, terr := c.takeOne(self, &entries[i], keep)
		switch {
		case terr != nil && failure == nil:
			failure = terr
		case f != nil:
			files = append(files, f)
		}
	}
	return files, failure
}

// takeOne takes one entry: kept, it is returned as a file; otherwise it is
// released where it lives.
func (c *pipeConn) takeOne(self syscall.Handle, e *passedEntry, keep bool) (*os.File, error) {
	if e.kind == entrySocket {
		s, err := wsaSocketFrom(&e.info)
		if err != nil {
			return nil, fdPassRefused("a socket could not be made from the sender's", err)
		}
		if !keep {
			// Beyond max: the socket was this process's from WSASocketW on.
			if cerr := syscall.Closesocket(s); cerr != nil {
				return nil, nil
			}
			return nil, nil
		}
		return os.NewFile(uintptr(s), "ipc-passed-socket"), nil
	}
	if c.proc == syscall.InvalidHandle {
		return nil, fdPassRefused("the peer's process cannot be opened", nil)
	}
	if !keep {
		// DUPLICATE_CLOSE_SOURCE closes the source whatever the call
		// returns, and nothing is duplicated here: its answer changes
		// nothing the receiver could act on.
		if cerr := syscall.DuplicateHandle(c.proc, e.handle, 0, nil, 0, false, duplicateCloseSource); cerr != nil {
			return nil, nil
		}
		return nil, nil
	}
	var mine syscall.Handle
	if err := syscall.DuplicateHandle(c.proc, e.handle, self, &mine, 0, false, duplicateSameAccess|duplicateCloseSource); err != nil {
		return nil, fdPassRefused("a file could not be taken from the sender", err)
	}
	return os.NewFile(uintptr(mine), "ipc-passed"), nil
}

// closeFiles closes files received but not handed to the caller.
func closeFiles(files []*os.File) {
	for _, f := range files {
		if err := f.Close(); err != nil {
			continue
		}
	}
}
