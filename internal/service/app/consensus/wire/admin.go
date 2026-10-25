package wire

import (
	"encoding/binary"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The membership changes an EntryMembership records.
const (
	// ChangeFounding is the founding configuration Bootstrap writes.
	ChangeFounding uint8 = iota + 1
	// ChangeAddLearner adds a learner.
	ChangeAddLearner
	// ChangePromote makes a learner a voter.
	ChangePromote
	// ChangeRemove removes a member and retires its id.
	ChangeRemove
	// ChangeUpdateAddress changes a member's address.
	ChangeUpdateAddress
	// ChangeDenyPin withdraws a member's SPKI pin.
	ChangeDenyPin
	// ChangeForced is the configuration Recover or RestoreSnapshot forces.
	ChangeForced
	// ChangeSnapshotLimit records the MaxSnapshotBytes a member runs with,
	// on that member's word alone: Target and the one member the change
	// carries name it, and the leader admits it from that member only.
	ChangeSnapshotLimit
)

// AdminVersion2 is the payload version of an engine entry whose admin
// carries Origin. A leader writes it once the committed cluster version is
// 2, never before: a node of protocol 1 reads version 0 only (D18, #561).
const AdminVersion2 uint32 = 2

// AdminVersion3 is the payload version of an engine entry whose admin
// carries, after version 2's Origin, the retired keys of its membership. A
// leader writes it once the committed cluster version is 3, never before: a
// node of protocol 2 reads versions 0 and 2 only (D18, #648).
const AdminVersion3 uint32 = 3

// AdminValue is the payload of every engine entry kind: who asked, what it expects
// to act on, and what it changes. Each kind reads the fields it needs.
type AdminValue struct {
	// Operator is the identity the caller vouched for.
	Operator string
	// Change is the membership change of an EntryMembership.
	Change uint8
	// Target is the node a change or a transfer names.
	Target coreconsensus.NodeID
	// Pre is the precondition of a clearing or raising operation.
	Pre coreconsensus.PreconditionSpec
	// Membership is the whole configuration after a membership change.
	Membership coreconsensus.MembershipValue
	// Contract is the timing contract of an EntryTimingContract.
	Contract coreconsensus.TimingContractSpec
	// ClusterVersion is the version an EntryClusterVersion raises to.
	ClusterVersion uint32
	// Origin is the authenticated node the leader received the operation
	// from: the member that forwarded it, or the leader itself. The leader
	// writes it, overwriting whatever the proposer wrote, and only in an
	// entry of payload version AdminVersion2 or AdminVersion3 (ADR 0152 D8,
	// #561).
	Origin coreconsensus.NodeID
}

// AppendAdmin appends the encoding of an engine entry's payload of version
// 0, which carries no Origin.
func AppendAdmin(dst []byte, a *AdminValue) []byte {
	dst = appendString(dst, a.Operator)
	dst = append(dst, a.Change)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(a.Target))
	dst = appendLogIdentity(dst, a.Pre.Log)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(a.Pre.Alarm))
	dst = binary.LittleEndian.AppendUint64(dst, a.Pre.Generation)
	dst = AppendMembership(dst, &a.Membership)
	dst = AppendTimingContract(dst, a.Contract)
	return binary.LittleEndian.AppendUint32(dst, a.ClusterVersion)
}

// AppendAdminAt appends the encoding of an engine entry's payload of
// version v: version 0's fields, then Origin from AdminVersion2, then the
// membership's retired keys from AdminVersion3. Another version is not
// written: the encoding of version 0 is returned for it.
func AppendAdminAt(dst []byte, v uint32, a *AdminValue) []byte {
	return appendAdminAt(dst, v, a)
}

// appendAdminAt is AppendAdminAt's body, apart so that AppendAdminAt stays
// a call the compiler inlines.
func appendAdminAt(dst []byte, v uint32, a *AdminValue) []byte {
	dst = AppendAdmin(dst, a)
	if v == AdminVersion2 || v == AdminVersion3 {
		dst = binary.LittleEndian.AppendUint64(dst, uint64(a.Origin))
	}
	if v == AdminVersion3 {
		dst = appendKeys(dst, a.Membership.RetiredKeys)
	}
	return dst
}

// ParseAdmin decodes an engine entry's payload of version 0.
func ParseAdmin(b []byte) (AdminValue, error) {
	return ParseAdminAt(0, b)
}

// ParseAdminAt decodes an engine entry's payload of version v, which the
// entry's Version names. A version this node does not know is
// VERSION_UNSUPPORTED, never read as another one.
func ParseAdminAt(v uint32, b []byte) (AdminValue, error) {
	if v != 0 && v != AdminVersion2 && v != AdminVersion3 {
		return AdminValue{}, errs.Wrap(coreconsensus.VersionUnsupported, errs.WrapParams{},
			errs.Int("admin_version", int(v)))
	}
	r := reader{b: b}
	var a AdminValue
	a.Operator = r.str()
	a.Change = r.u8()
	a.Target = coreconsensus.NodeID(r.u64())
	a.Pre.Log = r.logIdentity()
	a.Pre.Alarm = coreconsensus.AlarmID(r.u64())
	a.Pre.Generation = r.u64()
	a.Membership = r.membership()
	a.Contract = r.timing()
	a.ClusterVersion = r.u32()
	if v == AdminVersion2 || v == AdminVersion3 {
		a.Origin = coreconsensus.NodeID(r.u64())
	}
	if v == AdminVersion3 {
		a.Membership.RetiredKeys = r.keys()
	}
	if r.err == nil && len(r.b) != 0 {
		r.err = frameInvalid("admin trailing bytes", len(b))
	}
	return a, r.err
}
