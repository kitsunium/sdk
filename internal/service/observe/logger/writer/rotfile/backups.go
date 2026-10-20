package rotfile

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
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
			return wrapRotate(rerr, "service/observe/logger/writer/rotfile.shiftExisting: shifting a backup")
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
		return wrapRotate(rerr, "service/observe/logger/writer/rotfile.promoteActive: renaming active to .1")
	}
	//: active file is now the freshest backup.
	return nil
}

// gzipActive compresses the just-closed Path into Path.1.gz with mode 0600,
// then removes the uncompressed original. It runs while the sink is between
// descriptors (the active file is closed) so producers stall only for the
// duration of the compaction — documented as the producer-stall window.
//
// The archive is written to a fresh sibling os.CreateTemp makes — a random
// name opened O_CREATE|O_EXCL, 0600, which no link can stand in for — and
// renamed onto Path.1.gz. A rename replaces whatever entry is there and never
// follows it, so a link planted at Path.1.gz after shiftExisting moved the
// previous one away is replaced, not written through (CWE-59, #463); and a
// failed compression leaves no truncated Path.1.gz behind.
func (s *rotatingSink) gzipActive() error {
	final := s.cfg.Path + ".1.gz"
	//: the archive, complete and closed, under a name no link can stand in for.
	tmpName, werr := writeArchive(filepath.Dir(s.cfg.Path), filepath.Base(final)+".tmp-*", s.cfg.Path)
	//: nothing was published; writeArchive removed what it wrote.
	if werr != nil {
		//: the helper already wrapped its diagnostic.
		return werr
	}
	//: publish: the entry at Path.1.gz is replaced, never followed.
	if rerr := os.Rename(tmpName, final); rerr != nil {
		//: surface under the rotate sentinel, and drop the unpublished archive.
		return removeLeftover(tmpName, wrapRotate(rerr, "service/observe/logger/writer/rotfile.gzipActive: publishing .gz sibling"))
	}
	//: drop the uncompressed original now that the .gz is in place.
	if rerr := os.Remove(s.cfg.Path); rerr != nil && !os.IsNotExist(rerr) {
		//: a removal failure leaves a stray plaintext copy — surface it.
		return wrapRotate(rerr, "service/observe/logger/writer/rotfile.gzipActive: removing uncompressed original")
	}
	//: the archive is published and the original gone.
	return nil
}

// writeArchive compresses the file at srcPath into a fresh file os.CreateTemp
// makes in dir under pattern — O_CREATE|O_EXCL, 0600 — closes it, and returns
// its name. On failure it removes that file and returns no name, so a failed
// rotation leaves no partial archive behind.
func writeArchive(dir, pattern, srcPath string) (name string, err error) {
	tmp, terr := os.CreateTemp(dir, pattern)
	//: a create failure aborts the rotation.
	if terr != nil {
		//: surface the create failure under the rotate sentinel.
		return "", wrapRotate(terr, "service/observe/logger/writer/rotfile.gzipActive: creating .gz sibling")
	}
	//: closed on every path, before the caller renames it — Windows refuses to
	//: rename an open file; a late close failure replaces a nil err, so a
	//: truncated archive is never published; a failure removes the file.
	defer func() {
		//: the close error matters only when the body itself succeeded.
		if cerr := tmp.Close(); cerr != nil && err == nil {
			//: a close failure means the archive may be truncated.
			err = wrapRotate(cerr, "service/observe/logger/writer/rotfile.gzipActive: closing .gz sibling")
		}
		//: a failed archive is never handed to the caller.
		if err != nil {
			name, err = "", removeLeftover(tmp.Name(), err)
		}
	}()
	//: stream the closed active file through gzip into the sibling.
	if cerr := compressInto(tmp, srcPath); cerr != nil {
		//: the helper already wrapped its diagnostic.
		return "", cerr
	}
	//: the archive, closed by the deferred call.
	return tmp.Name(), nil
}

// removeLeftover removes the unpublished archive at name after cause failed
// the rotation, and returns cause — with the file named in a "leftover" field
// when it could not be removed, so the stray sibling is reported rather than
// silently left. Origin wins: the code stays the cause's.
func removeLeftover(name string, cause error) error {
	rerr := os.Remove(name)
	//: removed, or already gone.
	if rerr == nil || os.IsNotExist(rerr) {
		//: the rotation's own error.
		return cause
	}
	//: the cause, naming the file left behind and why.
	return errs.Wrap(cause, errs.WrapParams{}, errs.String("leftover", name), errs.String("leftover_error", rerr.Error()))
}

// compressInto streams the file at srcPath through gzip into dst. Factored out
// so gzipActive stays under KTN-FUNC-MAXLOC.
func compressInto(dst io.Writer, srcPath string) (err error) {
	//: reopen the closed active file read-only for streaming.
	src, oerr := os.Open(srcPath)
	//: a reopen failure aborts the compression.
	if oerr != nil {
		//: surface under the rotate sentinel.
		return wrapRotate(oerr, "service/observe/logger/writer/rotfile.compressInto: opening source")
	}
	//: release the source descriptor on every path, preserving a body error.
	defer func() {
		//: a source-close failure matters only when the copy itself succeeded.
		if cerr := src.Close(); cerr != nil && err == nil {
			//: surface the close failure under the rotate sentinel.
			err = wrapRotate(cerr, "service/observe/logger/writer/rotfile.compressInto: closing source")
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
		return wrapRotate(cerr, "service/observe/logger/writer/rotfile.compressInto: copying through gzip")
	}
	//: a trailer-close failure truncates the archive — surface it next.
	if zerr != nil {
		//: surface under the rotate sentinel.
		return wrapRotate(zerr, "service/observe/logger/writer/rotfile.compressInto: closing gzip writer")
	}
	//: compression stream complete.
	return nil
}
