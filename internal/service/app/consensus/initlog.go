package consensus

import (
	"crypto/rand"
	"encoding/binary"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxSlotLen bounds a slot name, which the stamp stores behind a one-byte
// length.
const maxSlotLen int = 32

// drawID draws a random NodeID, never 0.
func drawID() coreconsensus.NodeID {
	for {
		var b [8]byte
		_, _ = rand.Read(b[:])
		if id := coreconsensus.NodeID(binary.LittleEndian.Uint64(b[:])); id != 0 {
			return id
		}
	}
}

// drawIncarnation draws a random incarnation, which tells two lifetimes of
// one slot apart. crypto/rand.Read never returns an error on this toolchain
// (it aborts the process instead), so the discarded result hides nothing.
func drawIncarnation() coreconsensus.Incarnation {
	var inc coreconsensus.Incarnation
	_, _ = rand.Read(inc[:])
	return inc
}

// InitLog prepares a joining log purely offline: a random NodeID, a joining
// stamp, and the pair returned. It is idempotent: a log already joining for
// that cluster and slot returns its pair (D8, R3-02, R8-10).
func InitLog(log coreconsensus.LogStore, cluster coreconsensus.ClusterID, slot string) (coreconsensus.NodeID, coreconsensus.Incarnation, error) {
	st, err := log.Stamp()
	if err != nil {
		return 0, coreconsensus.Incarnation{}, storageFailed("stamp", err)
	}
	if st.State != 0 {
		if st.Log.Cluster == cluster && st.Slot == slot {
			return st.ID, st.Incarnation, nil
		}
		return 0, coreconsensus.Incarnation{}, origin(coreconsensus.DataDirMismatch, errs.String("why", "stamped for another cluster or slot"))
	}
	if !validSlot(slot) {
		return 0, coreconsensus.Incarnation{}, misconfigured("slot", "not [a-z0-9-]{1,32}")
	}
	st = coreconsensus.StampValue{
		Log: coreconsensus.LogIdentityValue{Cluster: cluster, Epoch: 1}, ID: drawID(),
		Incarnation: drawIncarnation(), Slot: slot, State: coreconsensus.StampJoining,
	}
	if err := log.WriteStamp(st); err != nil {
		return 0, coreconsensus.Incarnation{}, storageFailed("stamp", err)
	}
	return st.ID, st.Incarnation, nil
}

// RedrawLog draws a new NodeID, only while the stamp is joining and the log
// is empty (R8-10).
func RedrawLog(log coreconsensus.LogStore) (coreconsensus.NodeID, error) {
	st, err := log.Stamp()
	if err != nil {
		return 0, storageFailed("stamp", err)
	}
	if first, last := log.Bounds(); st.State != coreconsensus.StampJoining || last >= first {
		return 0, origin(coreconsensus.DataDirMismatch, errs.String("why", "not an empty joining directory"))
	}
	st.ID = drawID()
	if err := log.WriteStamp(st); err != nil {
		return 0, storageFailed("stamp", err)
	}
	return st.ID, nil
}

// validSlot admits a slot name of 1 to maxSlotLen bytes of [a-z0-9-]: it
// is written in the stamp and in a certificate's URI SAN, so nothing a
// filesystem, a URL or a log line could misread gets in.
func validSlot(s string) bool {
	if len(s) == 0 || len(s) > maxSlotLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
