// Package rotfile — backup-name arithmetic and the per-slot move/gzip helpers
// split out of rotate.go so each function stays under KTN-FUNC-MAXLOC.
package rotfile

import (
	"compress/gzip"
	"io"
	"os"
	"strconv"
)

// backupName returns the rotated sibling path for slot n (Path.n), or its
// gzip form (Path.n.gz) when Compress is set — the canonical on-disk name for
// that slot so removal and shifting agree.
func (s *rotatingSink) backupName(n int) string {
	//: the plain rotated name is always Path.<n>.
	base := s.cfg.Path + "." + strconv.Itoa(n)
	//: compressed runs carry the .gz suffix so eviction targets the right file.
	if s.cfg.Compress {
		//: the gzip sibling is the on-disk artefact for this slot.
		return base + ".gz"
	}
	//: uncompressed slot name.
	return base
}

// highestSlot reports the highest backup index to shift: MaxBackups-1 when a
// finite cap is set (the cap slot was already evicted), else a bounded probe
// that stops at the first missing slot so MaxBackups == 0 keeps all backups.
func (s *rotatingSink) highestSlot() int {
	//: a finite cap shifts everything below the (already-evicted) top slot.
	if s.cfg.MaxBackups > 0 {
		//: slots 1..MaxBackups-1 move down into 2..MaxBackups.
		return s.cfg.MaxBackups - 1
	}
	//: unbounded backups — probe upward until the first gap.
	n := 0
	//: walk slots until one is missing; the last present index is the top.
	for {
		//: stop probing at the first absent slot.
		if _, serr := os.Lstat(s.backupName(n + 1)); serr != nil {
			//: n is the highest existing backup (0 when none exist yet).
			return n
		}
		//: slot n+1 exists — keep climbing.
		n++
	}
}

// shiftExisting renames each existing backup down by one, highest index first
// so a lower slot never clobbers a higher one before it has moved. The caller
// holds s.mu.
func (s *rotatingSink) shiftExisting() error {
	//: iterate from the top slot down to 1 so moves never overwrite unmoved data.
	for n := s.highestSlot(); n >= 1; n-- {
		//: the source slot may be absent (sparse history) — skip it then.
		if _, serr := os.Lstat(s.backupName(n)); serr != nil {
			//: nothing at slot n; continue with the next lower slot.
			continue
		}
		//: move slot n into slot n+1.
		if rerr := os.Rename(s.backupName(n), s.backupName(n+1)); rerr != nil {
			//: a rename failure aborts the rotation.
			return wrapRotate(rerr, "service/writer/rotfile.shiftExisting: shifting a backup")
		}
	}
	//: all surviving backups shifted down by one.
	return nil
}

// promoteActive moves the just-closed active file into the .1 slot, gzipping it
// to Path.1.gz (0600) when Compress is set. The caller holds s.mu.
func (s *rotatingSink) promoteActive() error {
	//: the compressed path gzips Path into Path.1.gz and removes the original.
	if s.cfg.Compress {
		//: gzip + 0600 chmod happen inside the helper.
		return s.gzipActive()
	}
	//: uncompressed: a plain rename into the .1 slot.
	if rerr := os.Rename(s.cfg.Path, s.cfg.Path+".1"); rerr != nil {
		//: a rename failure aborts the rotation.
		return wrapRotate(rerr, "service/writer/rotfile.promoteActive: renaming active to .1")
	}
	//: active file is now the freshest backup.
	return nil
}

// gzipActive compresses the just-closed Path into Path.1.gz with mode 0600,
// then removes the uncompressed original. It runs while the sink is between
// descriptors (the active file is closed) so producers stall only for the
// duration of the compaction — documented as the producer-stall window.
func (s *rotatingSink) gzipActive() (err error) {
	//: create the .gz sibling with 0600 so gzip never leaks default perms.
	dst, derr := os.OpenFile(s.cfg.Path+".1.gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, defaultFilePerm)
	//: a create failure aborts the rotation.
	if derr != nil {
		//: surface the create failure under the rotate sentinel.
		return wrapRotate(derr, "service/writer/rotfile.gzipActive: creating .gz sibling")
	}
	//: close dst on every path; a late close failure replaces a nil err so a
	//: truncated archive is never reported as success.
	defer func() {
		//: capture the close error only when the body itself succeeded.
		if cerr := dst.Close(); cerr != nil && err == nil {
			//: a close failure means the .gz may be truncated — surface it.
			err = wrapRotate(cerr, "service/writer/rotfile.gzipActive: closing .gz sibling")
		}
	}()
	//: stream the closed active file through gzip into dst.
	if cerr := compressInto(dst, s.cfg.Path); cerr != nil {
		//: the helper already wrapped its diagnostic.
		return cerr
	}
	//: drop the uncompressed original now that the .gz is durable.
	if rerr := os.Remove(s.cfg.Path); rerr != nil && !os.IsNotExist(rerr) {
		//: a removal failure leaves a stray plaintext copy — surface it.
		return wrapRotate(rerr, "service/writer/rotfile.gzipActive: removing uncompressed original")
	}
	//: compaction body complete; the deferred close finalises the archive.
	return nil
}

// compressInto streams the file at srcPath through gzip into dst. Factored out
// so gzipActive stays under KTN-FUNC-MAXLOC.
func compressInto(dst io.Writer, srcPath string) (err error) {
	//: reopen the closed active file read-only for streaming.
	src, oerr := os.Open(srcPath)
	//: a reopen failure aborts the compression.
	if oerr != nil {
		//: surface under the rotate sentinel.
		return wrapRotate(oerr, "service/writer/rotfile.compressInto: opening source")
	}
	//: release the source descriptor on every path, preserving a body error.
	defer func() {
		//: a source-close failure matters only when the copy itself succeeded.
		if cerr := src.Close(); cerr != nil && err == nil {
			//: surface the close failure under the rotate sentinel.
			err = wrapRotate(cerr, "service/writer/rotfile.compressInto: closing source")
		}
	}()
	//: wrap dst in a gzip writer; closing it flushes the trailer.
	zw := gzip.NewWriter(dst)
	//: copy the plaintext through the compressor, then always close the writer.
	_, cerr := io.Copy(zw, src)
	//: close flushes + writes the trailer regardless of the copy outcome.
	zerr := zw.Close()
	//: a copy failure is the primary cause — surface it first.
	if cerr != nil {
		//: surface under the rotate sentinel.
		return wrapRotate(cerr, "service/writer/rotfile.compressInto: copying through gzip")
	}
	//: a trailer-close failure truncates the archive — surface it next.
	if zerr != nil {
		//: surface under the rotate sentinel.
		return wrapRotate(zerr, "service/writer/rotfile.compressInto: closing gzip writer")
	}
	//: compression stream complete.
	return nil
}
