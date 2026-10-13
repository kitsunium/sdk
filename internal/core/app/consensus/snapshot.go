package consensus

import (
	"context"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// SnapshotMetaValue describes one snapshot: where it cuts the log, the engine state
// it carries, and what its bytes must hash to. It is versioned; a version a
// node does not know is refused cleanly.
type SnapshotMetaValue struct {
	// ID names the snapshot, the same in a spool, an offer and a handle.
	ID string
	// Version is the snapshot format version, 1.
	Version uint8
	// Position is the last entry the snapshot covers.
	Position PositionValue
	// Log is the identity of the log the snapshot was cut from.
	Log LogIdentityValue
	// Membership is the configuration valid at Position, never a later one.
	Membership MembershipValue
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
	Timing TimingContractSpec
	// ClusterVersion is the committed cluster version at Position.
	ClusterVersion uint32
	// ClusterGeneration counts its raises.
	ClusterGeneration uint64
	// NoSpace is the no-space alarm raised at Position, 0 for none.
	NoSpace AlarmID
	// Reserved is carried uninterpreted for ADR 0173.
	Reserved []byte
}

// InstallOfferValue is what a leader sends before streaming a snapshot: the whole
// metadata first, so the receiver refuses a snapshot too large, of another
// version or already covered before a single byte of it is spooled (D7).
type InstallOfferValue struct {
	// Term is the sender's term.
	Term uint64
	// SnapshotID equals Meta.ID.
	SnapshotID string
	// Meta describes the snapshot.
	Meta SnapshotMetaValue
}

// InstallAcceptValue is the receiver's answer to an offer. The RECEIVER chooses the
// offset to resume from, from what it has durably spooled, so a resumed stream
// never appends to bytes the receiver did not keep (D7, R8-21).
type InstallAcceptValue struct {
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

// InstallParams is what the transport hands the engine once bytes flow, after
// the offer was accepted. Body ends with the connection: a stream cut short is a
// short read, and the spool keeps its last whole chunk for the next offer.
type InstallParams struct {
	// Ctx ends with the connection.
	Ctx context.Context
	// From is the peer TLS authenticated.
	From AuthenticatedPeerValue
	// Offer is the accepted offer.
	Offer InstallOfferValue
	// Offset is the accepted offset.
	Offset int64
	// Body is the encoded stream from Offset, ending with the connection.
	Body io.Reader
}

// AuthenticatedPeerValue is what the TLS handshake proved, never what a frame
// claims. The certificate proves the slot and the key; the ID and the
// incarnation are the member the transport admitted on that key. A peer that is
// not yet a member, during a JOIN, has ID 0 and only Slot and SPKI set.
type AuthenticatedPeerValue struct {
	// ID is the admitted member, 0 for a joiner.
	ID NodeID
	// Incarnation is that member's incarnation.
	Incarnation Incarnation
	// Slot is the slot the certificate's URI SAN names.
	Slot string
	// SPKI is the SHA-256 of the certificate's SubjectPublicKeyInfo.
	SPKI [32]byte
}

// JoinRequestValue is a pre-admission JOIN as a member received it: the slot and
// the key from TLS, the new node's id and incarnation from its frame.
type JoinRequestValue struct {
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

// FencedAttestationValue is the operator's statement that an old voter was fenced
// before a Recover: the voter, the method that fenced it, and one signed
// receipt per witness (one per NAS).
type FencedAttestationValue struct {
	// Voter is the old voter.
	Voter NodeID
	// Method is how it was fenced: "stopped", "powered-off" or "cut".
	Method string
	// Receipts are Ed25519 signatures, one per witness key, over the old
	// cluster id, the new cluster id and the voter.
	Receipts [][]byte
}
