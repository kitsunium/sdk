package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"strconv"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// cursorName names group's cursor on partition under cursors/: a digest of
// the group, so any group name is a safe file name of one length, and the
// partition.
func cursorName(group string, partition int) string {
	digest := sha256.Sum256([]byte(group))
	//: 32 hex digits are enough to tell groups apart.
	return hex.EncodeToString(digest[:16]) + "." + strconv.Itoa(partition)
}

// encodeCursor is a cursor file's bytes: the offset, then the group itself,
// so a digest collision is detected rather than read as the other group's.
func encodeCursor(group string, next corestream.Offset) []byte {
	out := binary.LittleEndian.AppendUint64(make([]byte, 0, 8+len(group)), uint64(next))
	//: the group after the offset.
	return append(out, group...)
}

// Commit records next as the offset group reads next on partition, in a
// file of its own published atomically and durably (vfs.WriteAtomic).
func (m *fileStream) Commit(
	_ context.Context, group string, partition int, next corestream.Offset,
) error {
	//: a cursor belongs to a named group.
	if group == "" {
		//: StreamMisconfigured, naming the argument.
		return misconfigured("group")
	}
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return refused
	}
	//: a closed stream commits nothing.
	if closed := m.usable(); closed != nil {
		//: the closed refusal.
		return closed
	}
	part := m.parts[partition]
	part.mu.RLock()
	head := part.next()
	part.mu.RUnlock()
	//: a cursor past every record is one from another stream.
	if next > head {
		//: OffsetAhead, with both offsets.
		return offsetAhead(next, head)
	}
	name := cursorName(group, partition)
	//: created, written, flushed, renamed, the directory flushed.
	if err := m.cursors.WriteAtomic(name, encodeCursor(group, next), fileMode); err != nil {
		//: the filesystem's refusal, in this engine's trail.
		return backendFailed("commit", name, err)
	}
	//: kept, below the first retained offset included: the next Read says so.
	return nil
}

// Committed returns the offset group committed last on partition.
func (m *fileStream) Committed(
	_ context.Context, group string, partition int,
) (corestream.Offset, error) {
	//: a cursor belongs to a named group.
	if group == "" {
		//: StreamMisconfigured, naming the argument.
		return 0, misconfigured("group")
	}
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return 0, refused
	}
	//: a closed stream answers nothing.
	if closed := m.usable(); closed != nil {
		//: the closed refusal.
		return 0, closed
	}
	name := cursorName(group, partition)
	data, err := fs.ReadFile(m.cursors, name)
	//: never a zero: where a new group starts is the reader's decision.
	if errors.Is(err, fs.ErrNotExist) {
		//: UnknownGroup.
		return 0, unknownGroup(group, partition)
	}
	//: the filesystem refused.
	if err != nil {
		//: in this engine's trail.
		return 0, backendFailed("committed", name, err)
	}
	//: a file this engine did not write, or another group's.
	if len(data) < 8 || !bytes.Equal(data[8:], []byte(group)) {
		//: StreamBackendFailed: refused rather than read as a cursor.
		return 0, backendFailed("cursor", name, nil)
	}
	//: the group's cursor.
	return corestream.Offset(binary.LittleEndian.Uint64(data)), nil
}
