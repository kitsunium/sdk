//go:build windows

package ipc

import (
	"encoding/binary"
	"io"
	"os"
	"syscall"
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
	// fileFrameMagic opens a frame of passed files: "SDKF", little-endian.
	fileFrameMagic uint32 = 0x464B4453
	// fileFrameHeader is the frame's head: the magic, the handle count and
	// the payload's length, three little-endian uint32.
	fileFrameHeader int = 12
	// handleBytes is one handle value in the frame, a little-endian uint64.
	handleBytes int = 8
	// maxPassedFiles bounds one frame, as Linux's SCM_MAX_FD bounds one
	// SCM_RIGHTS message.
	maxPassedFiles int = 253
)

// SendFiles sends payload with files attached, to a named peer only. A
// pipe carries bytes, not handles: each file is duplicated into this
// process's own table, private and not inheritable, and the frame — the
// magic, the count, the payload's length, the duplicates' values, the
// payload — tells the receiver which to take; RecvFiles duplicates each out
// of this process and closes it here (DUPLICATE_CLOSE_SOURCE). The receiver
// thus never wraps a number it was told: it takes what this process holds.
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
	dups := make([]syscall.Handle, 0, len(files))
	for _, f := range files {
		var dup syscall.Handle
		if err := syscall.DuplicateHandle(self, syscall.Handle(f.Fd()), self, &dup, 0, false, duplicateSameAccess); err != nil {
			closeHandles(dups...)
			return fdPassRefused("a file could not be duplicated", err)
		}
		dups = append(dups, dup)
	}
	n, err := c.File.Write(fileFrame(dups, payload))
	if err != nil {
		// Nothing written: the receiver cannot take the duplicates, so they
		// are closed here. Part of the frame written: the receiver may take
		// them, and a second close would reach a handle this process reused;
		// they are left to it.
		if n == 0 {
			closeHandles(dups...)
		}
		return fdPassRefused("the send failed", err)
	}
	return nil
}

// fileFrame lays out the frame SendFiles writes.
func fileFrame(dups []syscall.Handle, payload []byte) []byte {
	frame := make([]byte, fileFrameHeader, fileFrameHeader+len(dups)*handleBytes+len(payload))
	binary.LittleEndian.PutUint32(frame[0:], fileFrameMagic)
	binary.LittleEndian.PutUint32(frame[4:], uint32(len(dups)))
	binary.LittleEndian.PutUint32(frame[8:], uint32(len(payload)))
	for _, h := range dups {
		frame = binary.LittleEndian.AppendUint64(frame, uint64(h))
	}
	return append(frame, payload...)
}

// RecvFiles reads one frame SendFiles wrote: at most len(buf) bytes of its
// payload into buf — the rest stays in the stream, as on a Unix socket — and
// at most max of its files, each duplicated out of the sender's process,
// not inheritable, and closed there; those beyond max are closed there too
// and FDPassRefused "truncated" says so, beside the files that fit. The
// stream must be at a frame: on Windows a file passes in band.
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
	switch {
	case binary.LittleEndian.Uint32(head[0:]) != fileFrameMagic:
		return 0, nil, fdPassRefused("the stream is not at a frame of files", nil)
	case count > maxPassedFiles:
		return 0, nil, fdPassRefused("more files than one frame carries", nil)
	}
	values := make([]byte, count*handleBytes)
	if _, err := io.ReadFull(c.File, values); err != nil {
		return 0, nil, fdPassRefused("the receive failed", err)
	}
	n, rerr := io.ReadFull(c.File, buf[:min(size, len(buf))])
	files, perr := c.take(values, max)
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

// take duplicates the first max handles of values out of the peer's process
// into this one, closing each there, and closes the others there. Without a
// handle on the peer's process — an unnamed peer, or one this account may
// not open — nothing can be taken, and the sender keeps what it sent.
func (c *pipeConn) take(values []byte, max int) ([]*os.File, error) {
	if c.proc == syscall.InvalidHandle {
		return nil, fdPassRefused("the peer's process cannot be opened", nil)
	}
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return nil, fdPassRefused("this process has no handle", err)
	}
	var files []*os.File
	var failure error
	for i := 0; i+handleBytes <= len(values); i += handleBytes {
		source := syscall.Handle(binary.LittleEndian.Uint64(values[i:]))
		if len(files) >= max || failure != nil {
			// DUPLICATE_CLOSE_SOURCE closes the source whatever the call
			// returns, and nothing is duplicated here: its answer changes
			// nothing the receiver could act on.
			if cerr := syscall.DuplicateHandle(c.proc, source, 0, nil, 0, false, duplicateCloseSource); cerr != nil {
				continue
			}
			continue
		}
		var mine syscall.Handle
		if derr := syscall.DuplicateHandle(c.proc, source, self, &mine, 0, false, duplicateSameAccess|duplicateCloseSource); derr != nil {
			failure = fdPassRefused("a file could not be taken from the sender", derr)
			continue
		}
		files = append(files, os.NewFile(uintptr(mine), "ipc-passed"))
	}
	return files, failure
}

// closeFiles closes files received but not handed to the caller.
func closeFiles(files []*os.File) {
	for _, f := range files {
		if err := f.Close(); err != nil {
			continue
		}
	}
}
