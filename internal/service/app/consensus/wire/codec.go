package wire

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The frame header is frozen in every protocol version (ADR 0152 D18):
// length (u32) | version (u8) | type (u8) | group (u64), little-endian. length
// counts the bytes AFTER the length field. The two high bits of type are the
// lane.
const (
	frameHeaderLen  = 14
	frameVersion1   = 1
	tokenVersion1   = 1
	tokenLen        = 41
	metaVersion1    = 1
	entryFixedLen   = 8 + 8 + 1 + 4 + 4 + 4 + 4
	messageFixedLen = 1 + 8 + 8 + 8 + 8 + 8 + 8 + 1 + 8 + 8 + 8 + 8 + 8 + 4 + 4
)

// castagnoli is the CRC-32C table every checksum of the domain uses.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// BodySum returns the CRC-32C of an entry's payload, the checksum a proposer
// computes.
func BodySum(data []byte) uint32 { return crc32.Checksum(data, castagnoli) }

// ChainSum returns the chain value of an entry: the CRC-32C of the previous
// entry's chain value and of this entry's header fields. The LEADER computes
// it at its serialized append point; followers verify it.
func ChainSum(prev uint32, e *coreconsensus.Entry) uint32 {
	var b [4 + 8 + 8 + 1 + 4 + 4]byte
	binary.LittleEndian.PutUint32(b[0:], prev)
	binary.LittleEndian.PutUint64(b[4:], e.Position.Term)
	binary.LittleEndian.PutUint64(b[12:], e.Position.Index)
	b[20] = byte(e.Kind)
	binary.LittleEndian.PutUint32(b[21:], e.Version)
	binary.LittleEndian.PutUint32(b[25:], e.BodySum)
	return crc32.Checksum(b[:], castagnoli)
}

// Lane reads the lane of an encoded frame from its frozen header alone, so a
// transport routes a frame of a type or a version it does not parse.
func Lane(frame []byte) (coreconsensus.FrameLane, error) {
	if len(frame) < frameHeaderLen {
		return 0, frameInvalid("short header", len(frame))
	}
	return coreconsensus.FrameLane(frame[5] >> 6), nil
}

// AppendMessage appends m's encoding, frame header included, to dst.
func AppendMessage(dst []byte, m *coreconsensus.Message) []byte {
	start := len(dst)
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = append(dst, frameVersion1, byte(m.Kind))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.Group))
	dst = append(dst, byte(m.Flags))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.From))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.To))
	dst = binary.LittleEndian.AppendUint64(dst, m.Term)
	dst = binary.LittleEndian.AppendUint64(dst, m.LogTerm)
	dst = binary.LittleEndian.AppendUint64(dst, m.Index)
	dst = binary.LittleEndian.AppendUint64(dst, m.Commit)
	dst = append(dst, boolByte(m.Reject))
	dst = binary.LittleEndian.AppendUint64(dst, m.RejectHint)
	dst = binary.LittleEndian.AppendUint64(dst, m.Context)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.TransferIssuer))
	dst = binary.LittleEndian.AppendUint64(dst, m.TransferTerm)
	dst = binary.LittleEndian.AppendUint64(dst, m.Contract)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(m.Code))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(m.Entries)))
	for i := range m.Entries {
		dst = appendEntry(dst, &m.Entries[i])
	}
	binary.LittleEndian.PutUint32(dst[start:], uint32(len(dst)-start-4))
	return dst
}

// appendEntry appends one entry's wire form.
func appendEntry(dst []byte, e *coreconsensus.Entry) []byte {
	dst = binary.LittleEndian.AppendUint64(dst, e.Position.Term)
	dst = binary.LittleEndian.AppendUint64(dst, e.Position.Index)
	dst = append(dst, byte(e.Kind))
	dst = binary.LittleEndian.AppendUint32(dst, e.Version)
	dst = binary.LittleEndian.AppendUint32(dst, e.BodySum)
	dst = binary.LittleEndian.AppendUint32(dst, e.ChainSum)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(e.Data)))
	return append(dst, e.Data...)
}

// ParseMessage decodes one frame into m, reusing m.Entries' capacity. Entry
// data aliases frame: it is valid while frame is. A frame that does not parse
// is FRAME_INVALID; a frame version this node does not know is
// VERSION_UNSUPPORTED.
func ParseMessage(frame []byte, m *coreconsensus.Message) error {
	if len(frame) < frameHeaderLen {
		return frameInvalid("short header", len(frame))
	}
	if int(binary.LittleEndian.Uint32(frame)) != len(frame)-4 {
		return frameInvalid("length mismatch", len(frame))
	}
	if frame[4] != frameVersion1 {
		return errs.Wrap(coreconsensus.VersionUnsupported, errs.WrapParams{},
			errs.Int("frame_version", int(frame[4])))
	}
	kind := coreconsensus.MessageKind(frame[5])
	if !kind.Known() {
		return errs.Wrap(coreconsensus.VersionUnsupported, errs.WrapParams{},
			errs.Int("message_kind", int(kind)))
	}
	b := frame[frameHeaderLen:]
	if len(b) < messageFixedLen {
		return frameInvalid("short body", len(frame))
	}
	if b[49] > 1 {
		// One encoding per value: a non-canonical bool is refused, so an
		// accepted frame always encodes back to its own bytes.
		return frameInvalid("reject flag", len(frame))
	}
	entries := m.Entries[:0]
	*m = coreconsensus.Message{
		Version:        frame[4],
		Kind:           kind,
		Group:          coreconsensus.GroupID(binary.LittleEndian.Uint64(frame[6:])),
		Flags:          coreconsensus.MessageFlags(b[0]),
		From:           coreconsensus.NodeID(binary.LittleEndian.Uint64(b[1:])),
		To:             coreconsensus.NodeID(binary.LittleEndian.Uint64(b[9:])),
		Term:           binary.LittleEndian.Uint64(b[17:]),
		LogTerm:        binary.LittleEndian.Uint64(b[25:]),
		Index:          binary.LittleEndian.Uint64(b[33:]),
		Commit:         binary.LittleEndian.Uint64(b[41:]),
		Reject:         b[49] != 0,
		RejectHint:     binary.LittleEndian.Uint64(b[50:]),
		Context:        binary.LittleEndian.Uint64(b[58:]),
		TransferIssuer: coreconsensus.NodeID(binary.LittleEndian.Uint64(b[66:])),
		TransferTerm:   binary.LittleEndian.Uint64(b[74:]),
		Contract:       binary.LittleEndian.Uint64(b[82:]),
		Code:           errs.Code(binary.LittleEndian.Uint32(b[90:])),
	}
	n := binary.LittleEndian.Uint32(b[94:])
	b = b[messageFixedLen:]
	if uint64(n)*entryFixedLen > uint64(len(b)) {
		return frameInvalid("entry count", len(frame))
	}
	for i := uint32(0); i < n; i++ {
		var e coreconsensus.Entry
		var err error
		if b, err = parseEntry(b, &e); err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if len(b) != 0 {
		return frameInvalid("trailing bytes", len(frame))
	}
	m.Entries = entries
	return nil
}

// parseEntry decodes one entry from the head of b and returns the rest.
func parseEntry(b []byte, e *coreconsensus.Entry) ([]byte, error) {
	if len(b) < entryFixedLen {
		return nil, frameInvalid("short entry", len(b))
	}
	e.Position.Term = binary.LittleEndian.Uint64(b)
	e.Position.Index = binary.LittleEndian.Uint64(b[8:])
	e.Kind = coreconsensus.EntryKind(b[16])
	e.Version = binary.LittleEndian.Uint32(b[17:])
	e.BodySum = binary.LittleEndian.Uint32(b[21:])
	e.ChainSum = binary.LittleEndian.Uint32(b[25:])
	n := binary.LittleEndian.Uint32(b[29:])
	b = b[entryFixedLen:]
	if uint64(n) > uint64(len(b)) {
		return nil, frameInvalid("entry data", len(b))
	}
	e.Data = b[:n:n]
	return b[n:], nil
}

// AppendToken appends the 41-byte encoding of t: a version byte, the
// ClusterID, then epoch, term and index as big-endian uint64.
func AppendToken(dst []byte, t coreconsensus.Token) []byte {
	dst = append(dst, tokenVersion1)
	dst = append(dst, t.Cluster[:]...)
	dst = binary.BigEndian.AppendUint64(dst, t.Epoch)
	dst = binary.BigEndian.AppendUint64(dst, t.Term)
	return binary.BigEndian.AppendUint64(dst, t.Index)
}

// ParseToken decodes a token; another length or version is FRAME_INVALID.
func ParseToken(b []byte) (coreconsensus.Token, error) {
	if len(b) != tokenLen || b[0] != tokenVersion1 {
		return coreconsensus.Token{}, frameInvalid("token", len(b))
	}
	var t coreconsensus.Token
	copy(t.Cluster[:], b[1:17])
	t.Epoch = binary.BigEndian.Uint64(b[17:])
	t.Term = binary.BigEndian.Uint64(b[25:])
	t.Index = binary.BigEndian.Uint64(b[33:])
	return t, nil
}

// AppendSnapshotMeta appends the versioned encoding of m.
func AppendSnapshotMeta(dst []byte, m *coreconsensus.SnapshotMeta) []byte {
	dst = append(dst, metaVersion1)
	dst = appendString(dst, m.ID)
	dst = append(dst, m.Version)
	dst = binary.LittleEndian.AppendUint64(dst, m.Position.Term)
	dst = binary.LittleEndian.AppendUint64(dst, m.Position.Index)
	dst = appendLogIdentity(dst, m.Log)
	dst = AppendMembership(dst, &m.Membership)
	dst = binary.LittleEndian.AppendUint32(dst, m.ChainTip)
	dst = appendString(dst, m.Codec)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.EncodedSize))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.DecodedSize))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.ChunkSize))
	dst = append(dst, m.SHA256[:]...)
	dst = AppendTimingContract(dst, m.Timing)
	dst = binary.LittleEndian.AppendUint32(dst, m.ClusterVersion)
	dst = binary.LittleEndian.AppendUint64(dst, m.ClusterGeneration)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(m.NoSpace))
	return appendBytes(dst, m.Reserved)
}

// ParseSnapshotMeta decodes a SnapshotMeta, copying every byte it keeps.
func ParseSnapshotMeta(b []byte, m *coreconsensus.SnapshotMeta) error {
	r := reader{b: b}
	if v := r.u8(); v != metaVersion1 {
		if r.err != nil {
			return r.err
		}
		return errs.Wrap(coreconsensus.VersionUnsupported, errs.WrapParams{},
			errs.Int("snapshot_meta_version", int(v)))
	}
	var out coreconsensus.SnapshotMeta
	out.ID = r.str()
	out.Version = r.u8()
	out.Position.Term = r.u64()
	out.Position.Index = r.u64()
	out.Log = r.logIdentity()
	out.Membership = r.membership()
	out.ChainTip = r.u32()
	out.Codec = r.str()
	out.EncodedSize = int64(r.u64())
	out.DecodedSize = int64(r.u64())
	out.ChunkSize = int64(r.u64())
	copy(out.SHA256[:], r.n(32))
	out.Timing = r.timing()
	out.ClusterVersion = r.u32()
	out.ClusterGeneration = r.u64()
	out.NoSpace = coreconsensus.AlarmID(r.u64())
	out.Reserved = append([]byte(nil), r.bytes()...)
	if r.err == nil && len(r.b) != 0 {
		r.err = frameInvalid("snapshot meta trailing bytes", len(b))
	}
	if r.err != nil {
		return r.err
	}
	*m = out
	return nil
}

// AppendMembership appends the encoding of a membership, the payload of an
// EntryMembership and a part of SnapshotMeta.
func AppendMembership(dst []byte, m *coreconsensus.Membership) []byte {
	dst = binary.LittleEndian.AppendUint64(dst, m.Index)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(m.Members)))
	for i := range m.Members {
		x := &m.Members[i]
		dst = binary.LittleEndian.AppendUint64(dst, uint64(x.ID))
		dst = append(dst, x.Incarnation[:]...)
		dst = appendString(dst, x.Slot)
		dst = append(dst, x.SPKI[:]...)
		dst = appendString(dst, x.Address)
		dst = append(dst, boolByte(x.Voter), boolByte(x.Joining))
		dst = binary.LittleEndian.AppendUint32(dst, x.PayloadMax)
		dst = binary.LittleEndian.AppendUint64(dst, uint64(x.MaxSnapshotBytes))
	}
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(m.Retired)))
	for _, id := range m.Retired {
		dst = binary.LittleEndian.AppendUint64(dst, uint64(id))
	}
	return dst
}

// ParseMembership decodes a membership encoded by AppendMembership.
func ParseMembership(b []byte) (coreconsensus.Membership, error) {
	r := reader{b: b}
	m := r.membership()
	if r.err == nil && len(r.b) != 0 {
		r.err = frameInvalid("membership trailing bytes", len(b))
	}
	return m, r.err
}

// AppendTimingContract appends the encoding of a timing contract.
func AppendTimingContract(dst []byte, c coreconsensus.TimingContract) []byte {
	dst = binary.LittleEndian.AppendUint64(dst, c.Generation)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(c.HeartbeatInterval))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(c.ElectionTimeoutMin))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(c.ElectionTimeoutMax))
	return binary.LittleEndian.AppendUint64(dst, math.Float64bits(c.MaxClockDrift))
}

// ParseTimingContract decodes a timing contract.
func ParseTimingContract(b []byte) (coreconsensus.TimingContract, error) {
	r := reader{b: b}
	c := r.timing()
	if r.err == nil && len(r.b) != 0 {
		r.err = frameInvalid("contract trailing bytes", len(b))
	}
	return c, r.err
}

// appendLogIdentity appends the encoding of a log identity.
func appendLogIdentity(dst []byte, l coreconsensus.LogIdentity) []byte {
	dst = append(dst, l.Cluster[:]...)
	dst = binary.LittleEndian.AppendUint64(dst, l.Epoch)
	dst = append(dst, l.Predecessor[:]...)
	dst = binary.LittleEndian.AppendUint64(dst, l.PredecessorEpoch)
	return binary.LittleEndian.AppendUint64(dst, l.KeptIndex)
}

// appendString appends a u32-length-prefixed string.
func appendString(dst []byte, s string) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

// appendBytes appends u32-length-prefixed bytes.
func appendBytes(dst, b []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(b)))
	return append(dst, b...)
}

// boolByte encodes a bool as one byte.
func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// frameInvalid builds a FRAME_INVALID naming what failed.
func frameInvalid(what string, n int) error {
	return errs.Wrap(coreconsensus.FrameInvalid, errs.WrapParams{},
		errs.String("what", what), errs.Int("length", n))
}

// reader decodes little-endian fields, remembering the first failure so that
// a decoder reads straight through and checks once.
type reader struct {
	b   []byte
	err error
}

// n returns the next k bytes, or nil once a read failed.
func (r *reader) n(k int) []byte {
	if r.err != nil {
		return nil
	}
	if k < 0 || len(r.b) < k {
		r.err = frameInvalid("short field", len(r.b))
		return nil
	}
	out := r.b[:k:k]
	r.b = r.b[k:]
	return out
}

func (r *reader) u8() uint8 {
	if b := r.n(1); b != nil {
		return b[0]
	}
	return 0
}

// flag reads a canonical bool: 0 or 1, anything else a failure.
func (r *reader) flag() bool {
	v := r.u8()
	if v > 1 && r.err == nil {
		r.err = frameInvalid("bool", int(v))
	}
	return v == 1
}

func (r *reader) u32() uint32 {
	if b := r.n(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (r *reader) u64() uint64 {
	if b := r.n(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

func (r *reader) bytes() []byte {
	n := r.u32()
	if uint64(n) > uint64(len(r.b)) {
		r.err = frameInvalid("short bytes", len(r.b))
		return nil
	}
	return r.n(int(n))
}

func (r *reader) str() string { return string(r.bytes()) }

func (r *reader) logIdentity() coreconsensus.LogIdentity {
	var l coreconsensus.LogIdentity
	copy(l.Cluster[:], r.n(16))
	l.Epoch = r.u64()
	copy(l.Predecessor[:], r.n(16))
	l.PredecessorEpoch = r.u64()
	l.KeptIndex = r.u64()
	return l
}

func (r *reader) timing() coreconsensus.TimingContract {
	var c coreconsensus.TimingContract
	c.Generation = r.u64()
	c.HeartbeatInterval = time.Duration(r.u64())
	c.ElectionTimeoutMin = time.Duration(r.u64())
	c.ElectionTimeoutMax = time.Duration(r.u64())
	c.MaxClockDrift = math.Float64frombits(r.u64())
	return c
}

func (r *reader) membership() coreconsensus.Membership {
	var m coreconsensus.Membership
	m.Index = r.u64()
	n := r.u32()
	if uint64(n) > uint64(len(r.b)) {
		r.err = frameInvalid("member count", len(r.b))
		return m
	}
	for i := uint32(0); i < n && r.err == nil; i++ {
		var x coreconsensus.Member
		x.ID = coreconsensus.NodeID(r.u64())
		copy(x.Incarnation[:], r.n(16))
		x.Slot = r.str()
		copy(x.SPKI[:], r.n(32))
		x.Address = r.str()
		x.Voter = r.flag()
		x.Joining = r.flag()
		x.PayloadMax = r.u32()
		x.MaxSnapshotBytes = int64(r.u64())
		m.Members = append(m.Members, x)
	}
	k := r.u32()
	if uint64(k)*8 > uint64(len(r.b)) {
		r.err = frameInvalid("retired count", len(r.b))
		return m
	}
	for i := uint32(0); i < k; i++ {
		m.Retired = append(m.Retired, coreconsensus.NodeID(r.u64()))
	}
	return m
}
