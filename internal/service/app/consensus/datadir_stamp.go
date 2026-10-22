package consensus

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The stamp's encoding: the node's identity and its log's lineage, in the
// data directory's STAMP file.
const (
	// stampVersion is the first byte of an encoded stamp; a stamp of another
	// version is unreadable, never guessed at.
	stampVersion byte = 1
	// stampVersion2 is the first byte of a stamp whose log identity records
	// AdoptedFrom, which only Recover or RestoreSnapshot sets, in a history
	// at protocol version 2: version 1's fields with AdoptedFrom after the
	// kept index. A node of protocol 1 refuses to open it (#509).
	stampVersion2 byte = 2
	// stampFixed is the stamp's length before its slot and its CRC: version
	// 1, cluster 16, epoch 8, predecessor 16, predecessor epoch 8, kept
	// index 8, id 8, incarnation 16, state 1, founding 32, slot length 1.
	stampFixed int = 1 + 16 + 8 + 16 + 8 + 8 + 8 + 16 + 1 + 32 + 1
)

// encodeStamp is the stamp's on-disk form: the fixed fields, the slot
// prefixed by its length, and a CRC-32C of everything before it. A stamp
// whose log identity records AdoptedFrom is written in version 2, any
// other in version 1, so a value has one encoding.
func encodeStamp(s coreconsensus.StampValue) []byte {
	v2 := s.Log.AdoptedFrom != 0
	b := []byte{stampVersion}
	if v2 {
		b[0] = stampVersion2
	}
	b = append(b, s.Log.Cluster[:]...)
	b = binary.LittleEndian.AppendUint64(b, s.Log.Epoch)
	b = append(b, s.Log.Predecessor[:]...)
	b = binary.LittleEndian.AppendUint64(b, s.Log.PredecessorEpoch)
	b = binary.LittleEndian.AppendUint64(b, s.Log.KeptIndex)
	if v2 {
		b = binary.LittleEndian.AppendUint64(b, s.Log.AdoptedFrom)
	}
	b = binary.LittleEndian.AppendUint64(b, uint64(s.ID))
	b = append(b, s.Incarnation[:]...)
	b = append(b, byte(s.State))
	b = append(b, s.Founding[:]...)
	b = append(b, byte(len(s.Slot)))
	b = append(b, s.Slot...)
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

// decodeStamp parses encodeStamp's form; ok is false for another version, a
// short stamp, a CRC that fails, a state this version does not define, a
// slot length that disagrees with the bytes left, or a version 2 stamp
// that records no AdoptedFrom.
func decodeStamp(b []byte) (coreconsensus.StampValue, bool) {
	body, v2, ok := stampBody(b)
	if !ok {
		return coreconsensus.StampValue{}, false
	}
	var stamp coreconsensus.StampValue
	p, ok := decodeStampLog(body, v2, &stamp.Log)
	if !ok {
		return coreconsensus.StampValue{}, false
	}
	stamp.ID = coreconsensus.NodeID(binary.LittleEndian.Uint64(body[p:]))
	p += u64Bytes
	copy(stamp.Incarnation[:], body[p:])
	p += len(stamp.Incarnation)
	stamp.State = coreconsensus.StampState(body[p])
	if stamp.State < coreconsensus.StampJoining || stamp.State > coreconsensus.StampMember {
		// A state this version does not define is a corrupt or a newer
		// stamp: unreadable, never a directory Bootstrap may found.
		return coreconsensus.StampValue{}, false
	}
	p++
	copy(stamp.Founding[:], body[p:])
	p += len(stamp.Founding)
	n := int(body[p])
	p++
	if p+n != len(body) {
		return stamp, false
	}
	stamp.Slot = string(body[p:])
	return stamp, true
}

// stampBody checks a stamp's version, length and CRC, and returns the bytes
// the CRC covers and whether it is of version 2.
func stampBody(b []byte) (body []byte, v2, ok bool) {
	if len(b) == 0 || (b[0] != stampVersion && b[0] != stampVersion2) {
		return nil, false, false
	}
	v2 = b[0] == stampVersion2
	fixed := stampFixed
	if v2 {
		fixed += u64Bytes
	}
	if len(b) < fixed+4 {
		return nil, false, false
	}
	body = b[:len(b)-4]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(b[len(b)-4:]) {
		return nil, false, false
	}
	return body, v2, true
}

// decodeStampLog reads the log identity at the head of a stamp's body into
// l and returns the offset after it; ok is false for a version 2 stamp that
// records no AdoptedFrom, which encodeStamp writes in version 1.
func decodeStampLog(body []byte, v2 bool, l *coreconsensus.LogIdentityValue) (int, bool) {
	p := 1
	copy(l.Cluster[:], body[p:])
	p += len(l.Cluster)
	l.Epoch = binary.LittleEndian.Uint64(body[p:])
	p += u64Bytes
	copy(l.Predecessor[:], body[p:])
	p += len(l.Predecessor)
	l.PredecessorEpoch = binary.LittleEndian.Uint64(body[p:])
	p += u64Bytes
	l.KeptIndex = binary.LittleEndian.Uint64(body[p:])
	p += u64Bytes
	if !v2 {
		return p, true
	}
	l.AdoptedFrom = binary.LittleEndian.Uint64(body[p:])
	return p + u64Bytes, l.AdoptedFrom != 0
}

// readStamp reads the stamp of dir: none yet is the zero stamp, a read that
// fails is a STORAGE_FAILED, and a stamp that cannot be decoded is
// DATA_DIR_MISMATCH, since nothing in it can be trusted to name this node.
func readStamp(dir string) (coreconsensus.StampValue, error) {
	b, err := os.ReadFile(filepath.Join(dir, fileStamp))
	if errors.Is(err, os.ErrNotExist) {
		return coreconsensus.StampValue{}, nil
	}
	if err != nil {
		return coreconsensus.StampValue{}, storageFailed("stamp", err)
	}
	s, ok := decodeStamp(b)
	if !ok {
		return coreconsensus.StampValue{}, origin(coreconsensus.DataDirMismatch, errs.String("why", "unreadable stamp"))
	}
	return s, nil
}

// writeStamp replaces the stamp of dir atomically and durably.
func writeStamp(dir string, s coreconsensus.StampValue) error {
	return writeFileSync(filepath.Join(dir, fileStamp), encodeStamp(s))
}
