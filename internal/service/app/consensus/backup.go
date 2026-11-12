package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// An incremental backup (D17, #927): ExportBackup writes a snapshot once and
// the committed tail after it, AppendBackup the tail since a previous file,
// VerifyBackup checks the files offline and RestoreBackup restores them
// through ImportObserved. The recovery point is the last committed entry the
// backup node had applied when the last file was written.
//
// A file is a header, then records, then a trailer:
//
//	header  "KSBK", version 1, kind 'F' (export) or 'T' (tail)
//	record  type u8, length u32 LE, payload, CRC-32C u32 LE of the three
//
// An export holds snapshot records (the encoded stream SaveSnapshot wrote,
// in order), one meta record (wire.AppendSnapshotMeta), then entry records;
// a tail file holds entry records alone. An entry record is one
// wire.AppendMessage frame of kind MsgApp carrying a batch of entries. The
// trailer record names the point the file starts from, the point it ends
// at, and how many entries it holds; nothing follows it.

const (
	// backupMagic opens every backup file, export or tail.
	backupMagic = "KSBK"
	// backupVersion1 is the only layout version; a reader refuses any other.
	backupVersion1 byte = 1
	// backupExport is the file kind, written after the version, of an
	// export: a snapshot, then the committed tail after it.
	backupExport byte = 'F'
	// backupTail is the file kind of a tail: entries alone, after a point
	// an earlier file ended at.
	backupTail byte = 'T'
	// backupHeadLen is the magic, the version and the kind.
	backupHeadLen int = len(backupMagic) + 2

	// recSnapshot is a record of the snapshot's bytes, the first records
	// of an export (the record types are listed in the order an export
	// writes them).
	recSnapshot byte = 1
	// recMeta is the snapshot's metadata, or a tail's starting point.
	recMeta byte = 2
	// recEntries is one batch of committed entries.
	recEntries byte = 3
	// recTrailer is the last record: the file's end point and entry count.
	recTrailer byte = 4

	// recHeadLen is a record's type and length.
	recHeadLen int = 5
	// recSumLen is a record's CRC-32C, after its payload.
	recSumLen int = 4
	// backupChunk bounds a snapshot record.
	backupChunk int = 64 << 10
	// backupBatchBytes bounds the payloads of one entry record.
	backupBatchBytes int = 1 << 20
	// backupMaxRecord bounds any record a reader accepts.
	backupMaxRecord uint32 = 64 << 20
	// trailerLen is two points (term and index, 8 bytes each, and the 4-byte
	// chain value) then the 8-byte entry count.
	trailerLen int = 2*(8+8+4) + 8
)

// backupTable is the CRC-32C table of the records' sums.
var backupTable = crc32.MakeTable(crc32.Castagnoli)

// BackupPointValue is where a backup file ends, and where the next one starts:
// the position of the last entry it holds, or of the snapshot when it holds
// none, and the chain value at that position. AppendBackup takes it to
// write the tail after it.
type BackupPointValue struct {
	// Position is the last entry the backup holds.
	Position coreconsensus.PositionValue
	// ChainTip is the ChainSum at Position, which the next entry continues.
	ChainTip uint32
}

// BackupParams are the files RestoreBackup restores and VerifyBackup
// checks: the export, read twice (its metadata follows its snapshot), the
// tail files appended after it, in the order they were written, and the
// state machine the restore replays through. Export is required; Machine is
// required by RestoreBackup alone.
type BackupParams struct {
	// Export is the file ExportBackup wrote.
	Export io.ReadSeeker
	// Tails are the files AppendBackup wrote after it, in order.
	Tails []io.Reader
	// Machine is a fresh state machine; RestoreBackup restores into it.
	Machine coreconsensus.StateMachine
}

// ExportBackup writes a full backup to w: the snapshot SaveSnapshot cuts
// now, then the committed entries after it up to the commit at the call,
// once applied here, then a trailer. It returns the point the file ends at,
// for AppendBackup. A tail entry that changes the engine's own state (a
// cluster version, a timing contract, the no-space alarm) is refused, as
// ImportObserved would refuse it: export again once it is in a snapshot.
func (n *Node) ExportBackup(ctx context.Context, op coreconsensus.Operator, w io.Writer) (BackupPointValue, error) {
	bw := &backupWriter{w: w}
	if err := bw.header(backupExport); err != nil {
		return BackupPointValue{}, err
	}
	meta, err := n.SaveSnapshot(ctx, op, snapshotRecords{bw})
	if err != nil {
		return BackupPointValue{}, err
	}
	if err := bw.record(recMeta, wire.AppendSnapshotMeta(nil, &meta)); err != nil {
		return BackupPointValue{}, err
	}
	return n.writeBackupTail(ctx, bw, BackupPointValue{Position: meta.Position, ChainTip: meta.ChainTip})
}

// AppendBackup writes to w the committed entries after since, the point a
// previous ExportBackup or AppendBackup returned, up to the commit at the
// call, once applied here, then a trailer, and returns the point it ends
// at. since below the first entry the log keeps is COMPACTED: export again.
// An entry that does not continue since's chain is RESTORE_REFUSED.
func (n *Node) AppendBackup(ctx context.Context, since BackupPointValue, w io.Writer) (BackupPointValue, error) {
	bw := &backupWriter{w: w}
	if err := bw.header(backupTail); err != nil {
		return BackupPointValue{}, err
	}
	return n.writeBackupTail(ctx, bw, since)
}

// writeBackupTail writes the entries after base up to the commit a barrier
// reads now, checked against base's chain, then the trailer.
func (n *Node) writeBackupTail(ctx context.Context, bw *backupWriter, base BackupPointValue) (BackupPointValue, error) {
	tok, err := n.Barrier(ctx)
	if err != nil {
		return BackupPointValue{}, err
	}
	last, count := base, uint64(0)
	var frame []byte
	for last.Position.Index < tok.Index {
		es, err := n.ReadCommitted(ctx, last.Position.Index+1, tok.Index, backupBatchBytes)
		if err != nil {
			return BackupPointValue{}, err
		}
		if len(es) == 0 {
			break
		}
		if err := checkBackupBatch(last, es); err != nil {
			return BackupPointValue{}, err
		}
		frame = wire.AppendMessage(frame[:0], &coreconsensus.MessageValue{Kind: coreconsensus.MsgApp, Entries: es})
		if err := bw.record(recEntries, frame); err != nil {
			return BackupPointValue{}, err
		}
		e := &es[len(es)-1]
		last, count = BackupPointValue{Position: e.Position, ChainTip: e.ChainSum}, count+uint64(len(es))
	}
	if err := bw.record(recTrailer, appendTrailer(nil, base, last, count)); err != nil {
		return BackupPointValue{}, err
	}
	return last, nil
}

// VerifyBackup checks a backup offline, writing nothing: each record's sum,
// the export's snapshot against its metadata (sizes and SHA-256, decoded as
// OpenSnapshot does, bounded by opt.MaxDecoded), and every entry of the
// export and of the tails, in order, against the chain from the snapshot's
// ChainTip. It returns the point the backup restores to. opt.Expected is
// ignored: the export carries the metadata.
func VerifyBackup(p *BackupParams, opt *RestoreOptions) (BackupPointValue, error) {
	if opt == nil || p == nil || p.Export == nil {
		return BackupPointValue{}, restoreRefused("no options or no export")
	}
	meta, at, _, err := scanBackup(p, false)
	if err != nil {
		return BackupPointValue{}, err
	}
	sr, err := backupSnapshot(p.Export)
	if err != nil {
		return BackupPointValue{}, err
	}
	o := *opt
	o.Expected = meta
	r, err := OpenSnapshot(sr, &o)
	if err != nil {
		return BackupPointValue{}, err
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return BackupPointValue{}, err
	}
	return at, nil
}

// RestoreBackup starts a new history on an empty, joining directory from a
// backup: the files are checked as VerifyBackup checks them, the chain
// before anything is written, then the export's snapshot and every entry
// after it are handed to ImportObserved, which replays them through
// p.Machine and restores the state they reach, its last entry the point
// VerifyBackup returns. opt.Expected is ignored: the export carries the
// metadata.
func RestoreBackup(d DataDir, p *BackupParams, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RestoreOptions) (coreconsensus.SnapshotMetaValue, error) {
	if opt == nil || p == nil || p.Export == nil || p.Machine == nil {
		return coreconsensus.SnapshotMetaValue{}, restoreRefused("no options, no export or no state machine")
	}
	meta, _, tail, err := scanBackup(p, true)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	sr, err := backupSnapshot(p.Export)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	o := *opt
	o.Expected = meta
	return ImportObserved(d, &ImportParams{Snapshot: sr, Tail: tail, Machine: p.Machine}, newCluster, members, &o)
}

// scanBackup reads the export then each tail, checks every record and the
// chain across them, and returns the export's metadata, the point the last
// file ends at and, when keep, the entries in order.
func scanBackup(p *BackupParams, keep bool) (meta coreconsensus.SnapshotMetaValue, at BackupPointValue, tail []coreconsensus.EntryValue, err error) {
	if _, err := p.Export.Seek(0, io.SeekStart); err != nil {
		return coreconsensus.SnapshotMetaValue{}, BackupPointValue{}, nil, err
	}
	s := &backupScan{keep: keep}
	if err := s.file(p.Export, backupExport); err != nil {
		return coreconsensus.SnapshotMetaValue{}, BackupPointValue{}, nil, err
	}
	for i, t := range p.Tails {
		if t == nil {
			return coreconsensus.SnapshotMetaValue{}, BackupPointValue{}, nil, restoreRefused(fmt.Sprintf("tail %d is nil", i))
		}
		if err := s.file(t, backupTail); err != nil {
			return coreconsensus.SnapshotMetaValue{}, BackupPointValue{}, nil, err
		}
	}
	return s.meta, s.at, s.tail, nil
}

// backupScan is scanBackup's state across files.
type backupScan struct {
	keep bool
	meta coreconsensus.SnapshotMetaValue
	at   BackupPointValue
	tail []coreconsensus.EntryValue
	msg  coreconsensus.MessageValue
}

// file reads one backup file of the kind given to its trailer.
func (s *backupScan) file(r io.Reader, kind byte) error {
	br := &backupReader{r: r}
	if err := br.header(kind); err != nil {
		return err
	}
	f := backupFile{kind: kind, base: s.at, metaSeen: kind != backupExport}
	for {
		typ, payload, err := br.next(s.keep)
		if err != nil {
			return err
		}
		done, err := s.record(br, &f, typ, payload)
		if done || err != nil {
			return err
		}
	}
}

// backupFile is what file has read of one backup file so far: its kind, the
// point it starts from, whether the metadata record came and how many
// entries it held.
type backupFile struct {
	kind     byte
	base     BackupPointValue
	count    uint64
	metaSeen bool
}

// record applies one record of a file to the scan and to f, and reports
// done once the trailer, the last record, has been checked.
func (s *backupScan) record(br *backupReader, f *backupFile, typ byte, payload []byte) (done bool, err error) {
	if !recordInPlace(typ, f) {
		return false, snapshotCorrupt(fmt.Sprintf("backup record %d out of place", typ))
	}
	switch typ {
	case recMeta:
		if err := wire.ParseSnapshotMeta(payload, &s.meta); err != nil {
			return false, err
		}
		f.metaSeen = true
		s.at = BackupPointValue{Position: s.meta.Position, ChainTip: s.meta.ChainTip}
		f.base = s.at
	case recEntries:
		if err := s.entries(payload); err != nil {
			return false, err
		}
		f.count += uint64(len(s.msg.Entries))
	case recTrailer:
		return true, s.trailer(br, payload, f.base, f.count)
	}
	return false, nil
}

// recordInPlace reports whether a record of type typ may come next in f:
// snapshot bytes in an export before its metadata, the metadata once, then
// entries and the trailer after it.
func recordInPlace(typ byte, f *backupFile) bool {
	switch typ {
	case recSnapshot:
		return f.kind == backupExport && !f.metaSeen
	case recMeta:
		return !f.metaSeen
	case recEntries, recTrailer:
		return f.metaSeen
	}
	return false
}

// entries checks one entry record against the chain and keeps its entries.
func (s *backupScan) entries(payload []byte) error {
	if err := wire.ParseMessage(payload, &s.msg); err != nil {
		return err
	}
	if s.msg.Kind != coreconsensus.MsgApp || len(s.msg.Entries) == 0 {
		return snapshotCorrupt("backup entry record")
	}
	if err := checkBackupBatch(s.at, s.msg.Entries); err != nil {
		return err
	}
	if s.keep {
		s.tail = append(s.tail, s.msg.Entries...)
	}
	e := &s.msg.Entries[len(s.msg.Entries)-1]
	s.at = BackupPointValue{Position: e.Position, ChainTip: e.ChainSum}
	return nil
}

// trailer checks a file's trailer against what the file held, and that
// nothing follows it.
func (s *backupScan) trailer(br *backupReader, payload []byte, base BackupPointValue, count uint64) error {
	from, to, n, ok := parseTrailer(payload)
	switch {
	case !ok:
		return snapshotCorrupt("backup trailer")
	case from != base:
		return restoreRefused(fmt.Sprintf("the backup file starts at %d, not at %d where the one before ends", from.Position.Index, base.Position.Index))
	case to != s.at || n != count:
		return snapshotCorrupt("backup trailer differs from the entries")
	}
	var one [1]byte
	if _, err := io.ReadFull(br.r, one[:]); !errors.Is(err, io.EOF) {
		return snapshotCorrupt("bytes after the backup trailer")
	}
	return nil
}

// checkBackupBatch checks that es continues at, as ImportObserved checks a
// tail against a snapshot.
func checkBackupBatch(at BackupPointValue, es []coreconsensus.EntryValue) error {
	return checkObservedTail(&coreconsensus.SnapshotMetaValue{Position: at.Position, ChainTip: at.ChainTip}, es)
}

// backupSnapshot rewinds an export and returns the reader of its snapshot
// records' bytes.
func backupSnapshot(export io.ReadSeeker) (io.Reader, error) {
	if _, err := export.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	br := &backupReader{r: export}
	if err := br.header(backupExport); err != nil {
		return nil, err
	}
	return &backupSnapReader{br: br}, nil
}

// appendTrailer appends a trailer's payload.
func appendTrailer(dst []byte, from, to BackupPointValue, count uint64) []byte {
	for _, p := range [2]BackupPointValue{from, to} {
		dst = binary.LittleEndian.AppendUint64(dst, p.Position.Term)
		dst = binary.LittleEndian.AppendUint64(dst, p.Position.Index)
		dst = binary.LittleEndian.AppendUint32(dst, p.ChainTip)
	}
	return binary.LittleEndian.AppendUint64(dst, count)
}

// parseTrailer reads a trailer's payload.
func parseTrailer(b []byte) (from, to BackupPointValue, count uint64, ok bool) {
	if len(b) != trailerLen {
		return BackupPointValue{}, BackupPointValue{}, 0, false
	}
	var ps [2]BackupPointValue
	for i := range ps {
		ps[i].Position.Term = binary.LittleEndian.Uint64(b)
		ps[i].Position.Index = binary.LittleEndian.Uint64(b[8:])
		ps[i].ChainTip = binary.LittleEndian.Uint32(b[16:])
		b = b[20:]
	}
	return ps[0], ps[1], binary.LittleEndian.Uint64(b), true
}

// backupWriter writes a backup file's header and records.
type backupWriter struct {
	w   io.Writer
	buf []byte
}

// header writes the file's header.
func (b *backupWriter) header(kind byte) error {
	_, err := b.w.Write(append([]byte(backupMagic), backupVersion1, kind))
	return err
}

// record writes one record, its sum after it.
func (b *backupWriter) record(typ byte, payload []byte) error {
	b.buf = append(b.buf[:0], typ)
	b.buf = binary.LittleEndian.AppendUint32(b.buf, uint32(len(payload)))
	b.buf = append(b.buf, payload...)
	b.buf = binary.LittleEndian.AppendUint32(b.buf, crc32.Checksum(b.buf, backupTable))
	_, err := b.w.Write(b.buf)
	return err
}

// snapshotRecords is the writer SaveSnapshot copies the encoded stream to:
// each write becomes snapshot records of at most backupChunk bytes.
type snapshotRecords struct{ bw *backupWriter }

// Write implements io.Writer.
func (s snapshotRecords) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += backupChunk {
		if err := s.bw.record(recSnapshot, p[off:min(off+backupChunk, len(p))]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

// backupReader reads a backup file's header and records.
type backupReader struct {
	r   io.Reader
	buf []byte
}

// header reads the file's header and checks it names kind.
func (b *backupReader) header(kind byte) error {
	var h [backupHeadLen]byte
	if _, err := io.ReadFull(b.r, h[:]); err != nil {
		return snapshotCorrupt("short backup header")
	}
	switch {
	case !bytes.Equal(h[:len(backupMagic)], []byte(backupMagic)):
		return snapshotCorrupt("not a backup file")
	case h[len(backupMagic)] != backupVersion1:
		return snapshotCorrupt("backup version")
	case h[len(backupMagic)+1] != kind:
		return restoreRefused(fmt.Sprintf("a backup file of kind %q where %q is due", h[len(backupMagic)+1], kind))
	}
	return nil
}

// next reads one record and checks its sum. The payload aliases the
// reader's buffer, valid until the next call, unless fresh asks for a
// buffer of its own.
func (b *backupReader) next(fresh bool) (byte, []byte, error) {
	var h [recHeadLen]byte
	if _, err := io.ReadFull(b.r, h[:]); err != nil {
		return 0, nil, snapshotCorrupt("backup cut before its trailer")
	}
	n := binary.LittleEndian.Uint32(h[1:])
	if n > backupMaxRecord {
		return 0, nil, snapshotCorrupt("backup record length")
	}
	size := int(n) + recSumLen
	if fresh || cap(b.buf) < size {
		b.buf = make([]byte, 0, size)
	}
	buf := b.buf[:size]
	if _, err := io.ReadFull(b.r, buf); err != nil {
		return 0, nil, snapshotCorrupt("backup cut in a record")
	}
	sum := crc32.Update(crc32.Checksum(h[:], backupTable), backupTable, buf[:n])
	if sum != binary.LittleEndian.Uint32(buf[n:]) {
		return 0, nil, snapshotCorrupt("backup record sum")
	}
	return h[0], buf[:n:n], nil
}

// backupSnapReader reads the bytes of an export's snapshot records, and
// io.EOF at the first record that is not one.
type backupSnapReader struct {
	br   *backupReader
	cur  []byte
	done bool
}

// Read implements io.Reader.
func (s *backupSnapReader) Read(p []byte) (int, error) {
	for len(s.cur) == 0 {
		if s.done {
			return 0, io.EOF
		}
		typ, payload, err := s.br.next(false)
		if err != nil {
			return 0, err
		}
		if typ != recSnapshot {
			s.done = true
			return 0, io.EOF
		}
		s.cur = payload
	}
	n := copy(p, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}
