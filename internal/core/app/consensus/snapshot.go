package consensus

import (
	"context"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// SnapshotMeta describes one snapshot: where it cuts the log, the engine state
// it carries, and what its bytes must hash to. It is versioned; a version a
// node does not know is refused cleanly.
type SnapshotMeta struct {
	// ID names the snapshot, the same in a spool, an offer and a handle.
	ID string
	// Version is the snapshot format version, 1.
	Version uint8
	// Position is the last entry the snapshot covers.
	Position Position
	// Log is the identity of the log the snapshot was cut from.
	Log LogIdentity
	// Membership is the configuration valid at Position, never a later one.
	Membership Membership
	// ChainTip is the authenticated chain value at Position: the chain is
	// carried across the boundary, never reseeded.
	ChainTip uint32
	// Codec names the compression of the chunks: "flate" or "gzip".
	Codec string
	// EncodedSize is the size of the encoded stream, chunk headers included.
	EncodedSize int64
	// DecodedSize is the size of the decoded stream.
	DecodedSize int64
	// ChunkSize is the largest decoded chunk.
	ChunkSize int64
	// SHA256 is the digest of the DECODED stream, computed by the engine.
	SHA256 [32]byte
	// Timing is the committed timing contract at Position.
	Timing TimingContract
	// ClusterVersion is the committed cluster version at Position.
	ClusterVersion uint32
	// ClusterGeneration counts its raises.
	ClusterGeneration uint64
	// NoSpace is the no-space alarm raised at Position, 0 for none.
	NoSpace AlarmID
	// Reserved is carried uninterpreted for ADR 0173.
	Reserved []byte
}

// InstallOffer is what a leader sends before streaming a snapshot.
type InstallOffer struct {
	// Term is the sender's term.
	Term uint64
	// SnapshotID equals Meta.ID.
	SnapshotID string
	// Meta describes the snapshot.
	Meta SnapshotMeta
}

// InstallAccept is the receiver's answer to an offer.
type InstallAccept struct {
	// SnapshotID is the snapshot answered.
	SnapshotID string
	// Offset is the chunk-aligned ENCODED offset to resume from, 0 for a
	// fresh spool.
	Offset int64
	// Refused is zero, or why the offer is refused.
	Refused errs.Code
	// Held reports that the receiver already holds the snapshot: an
	// idempotent success.
	Held bool
}

// InstallRequest is what the transport hands the engine once bytes flow.
type InstallRequest struct {
	// Ctx ends with the connection.
	Ctx context.Context
	// From is the peer TLS authenticated.
	From AuthenticatedPeer
	// Offer is the accepted offer.
	Offer InstallOffer
	// Offset is the accepted offset.
	Offset int64
	// Body is the encoded stream from Offset, ending with the connection.
	Body io.Reader
}

// AuthenticatedPeer is what the TLS handshake proved, never what a frame
// claims. The certificate proves the slot and the key; the ID and the
// incarnation are the member the transport admitted on that key. A peer that is
// not yet a member, during a JOIN, has ID 0 and only Slot and SPKI set.
type AuthenticatedPeer struct {
	// ID is the admitted member, 0 for a joiner.
	ID NodeID
	// Incarnation is that member's incarnation.
	Incarnation Incarnation
	// Slot is the slot the certificate's URI SAN names.
	Slot string
	// SPKI is the SHA-256 of the certificate's SubjectPublicKeyInfo.
	SPKI [32]byte
}

// JoinRequest is a pre-admission JOIN as a member received it: the slot and
// the key from TLS, the new node's id and incarnation from its frame.
type JoinRequest struct {
	// Cluster is the cluster the joiner claims.
	Cluster ClusterID
	// ID is the joiner's new NodeID.
	ID NodeID
	// Incarnation is the joiner's incarnation.
	Incarnation Incarnation
	// Slot is the slot its certificate names.
	Slot string
	// SPKI is its key, from the handshake.
	SPKI [32]byte
	// Address is where it listens.
	Address string
}

// JoinConfig is what a joining node needs to send its JOIN.
type JoinConfig struct {
	// Addresses are the members to try, in order.
	Addresses []string
	// Stamp is the joiner's stamp, from Init.
	Stamp Stamp
	// Address is where the joiner listens.
	Address string
	// Pins are the SPKIs of the members the joiner trusts, from the
	// operator. An answer counts only from a peer presenting one of them,
	// and only when the membership it returns lists that peer under that
	// key; an empty list refuses the JOIN.
	Pins [][32]byte
}

// FencedAttestation is the operator's statement that an old voter was fenced
// before a Recover: the voter, the method that fenced it, and one signed
// receipt per witness (one per NAS).
type FencedAttestation struct {
	// Voter is the old voter.
	Voter NodeID
	// Method is how it was fenced: "stopped", "powered-off" or "cut".
	Method string
	// Receipts are Ed25519 signatures, one per witness key, over the old
	// cluster id, the new cluster id and the voter.
	Receipts [][]byte
}
