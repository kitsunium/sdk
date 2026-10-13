package wire

import (
	"encoding/binary"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The membership changes an EntryMembership records.
const (
	// ChangeFounding is the founding configuration Bootstrap writes.
	ChangeFounding uint8 = 1
	// ChangeAddLearner adds a learner.
	ChangeAddLearner uint8 = 2
	// ChangePromote makes a learner a voter.
	ChangePromote uint8 = 3
	// ChangeRemove removes a member and retires its id.
	ChangeRemove uint8 = 4
	// ChangeUpdateAddress changes a member's address.
	ChangeUpdateAddress uint8 = 5
	// ChangeDenyPin withdraws a member's SPKI pin.
	ChangeDenyPin uint8 = 6
	// ChangeForced is the configuration Recover or RestoreSnapshot forces.
	ChangeForced uint8 = 7
)

// Admin is the payload of every engine entry kind: who asked, what it expects
// to act on, and what it changes. Each kind reads the fields it needs.
type Admin struct {
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
}

// AppendAdmin appends the encoding of an engine entry's payload.
func AppendAdmin(dst []byte, a *Admin) []byte {
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

// ParseAdmin decodes an engine entry's payload.
func ParseAdmin(b []byte) (Admin, error) {
	r := reader{b: b}
	var a Admin
	a.Operator = r.str()
	a.Change = r.u8()
	a.Target = coreconsensus.NodeID(r.u64())
	a.Pre.Log = r.logIdentity()
	a.Pre.Alarm = coreconsensus.AlarmID(r.u64())
	a.Pre.Generation = r.u64()
	a.Membership = r.membership()
	a.Contract = r.timing()
	a.ClusterVersion = r.u32()
	if r.err == nil && len(r.b) != 0 {
		r.err = frameInvalid("admin trailing bytes", len(b))
	}
	return a, r.err
}
