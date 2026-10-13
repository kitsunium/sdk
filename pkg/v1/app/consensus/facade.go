package consensus

import (
	"context"
	"crypto/x509"
	"io"
	"net/url"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	svcconsensus "github.com/kitsunium/sdk/internal/service/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Every type needed to implement a published port is aliased here (R2-32).
type (
	NodeID            = coreconsensus.NodeID
	Incarnation       = coreconsensus.Incarnation
	ClusterID         = coreconsensus.ClusterID
	LogIdentity       = coreconsensus.LogIdentity
	GroupID           = coreconsensus.GroupID
	Position          = coreconsensus.Position
	Entry             = coreconsensus.Entry
	Token             = coreconsensus.Token
	EntryKind         = coreconsensus.EntryKind
	HardState         = coreconsensus.HardState
	Stamp             = coreconsensus.Stamp
	StampState        = coreconsensus.StampState
	Message           = coreconsensus.Message
	MessageKind       = coreconsensus.MessageKind
	FrameLane         = coreconsensus.FrameLane
	AuthenticatedPeer = coreconsensus.AuthenticatedPeer
	InstallOffer      = coreconsensus.InstallOffer
	InstallAccept     = coreconsensus.InstallAccept
	InstallRequest    = coreconsensus.InstallRequest
	StopReason        = coreconsensus.StopReason
	JoinConfig        = coreconsensus.JoinConfig
	JoinRequest       = coreconsensus.JoinRequest
	Joiner            = coreconsensus.Joiner
	JoinAuthorizer    = coreconsensus.JoinAuthorizer
	FencedAttestation = coreconsensus.FencedAttestation
	SnapshotMeta      = coreconsensus.SnapshotMeta
	SnapshotSink      = coreconsensus.SnapshotSink
	SnapshotHandle    = coreconsensus.SnapshotHandle
	TimingContract    = coreconsensus.TimingContract
	Versions          = coreconsensus.Versions
	Member            = coreconsensus.Member
	Membership        = coreconsensus.Membership
	Operator          = coreconsensus.Operator
	Status            = coreconsensus.Status
	Role              = coreconsensus.Role
	AlarmID           = coreconsensus.AlarmID
	Precondition      = coreconsensus.Precondition

	Replicated           = coreconsensus.Replicated
	StateMachine         = coreconsensus.StateMachine
	DurableStateMachine  = coreconsensus.DurableStateMachine
	BatchingStateMachine = coreconsensus.BatchingStateMachine
	Snapshot             = coreconsensus.Snapshot
	LogReader            = coreconsensus.LogReader
	LogStore             = coreconsensus.LogStore
	SnapshotStore        = coreconsensus.SnapshotStore
	Transport            = coreconsensus.Transport
	UnreachableReporter  = coreconsensus.UnreachableReporter
	LeadershipWatcher    = coreconsensus.LeadershipWatcher

	Health          = svcconsensus.Health
	PeerHealth      = svcconsensus.PeerHealth
	ReadMode        = svcconsensus.ReadMode
	Durability      = svcconsensus.Durability
	Config          = svcconsensus.Config
	Node            = svcconsensus.Node
	MemoryLog       = svcconsensus.MemoryLog
	MemorySnapshots = svcconsensus.MemorySnapshots
	MemoryNetwork   = svcconsensus.MemoryNetwork
	MemoryTransport = svcconsensus.MemoryTransport

	DataDir         = svcconsensus.DataDir
	DataDirOptions  = svcconsensus.DataDirOptions
	Report          = svcconsensus.Report
	QuarantineExit  = svcconsensus.QuarantineExit
	RecoverOptions  = svcconsensus.RecoverOptions
	RestoreOptions  = svcconsensus.RestoreOptions
	TransportConfig = svcconsensus.TransportConfig
	TCPTransport    = svcconsensus.TCPTransport
	PEMIdentity     = svcconsensus.PEMIdentity
)

// The two exits of ClearQuarantine.
const (
	WipeAndRejoin         = svcconsensus.WipeAndRejoin
	TruncateAndStaySilent = svcconsensus.TruncateAndStaySilent
)

// OpenDataDir takes the directory lock first, then recovers the log.
func OpenDataDir(dir string) (DataDir, error) { return svcconsensus.OpenDataDir(dir) }

// OpenDataDirWith is OpenDataDir with options.
func OpenDataDirWith(dir string, opts DataDirOptions) (DataDir, error) {
	return svcconsensus.OpenDataDirWith(dir, opts)
}

// Init prepares a joining node offline: a random NodeID and a joining stamp.
func Init(d DataDir, cluster ClusterID, slot string) (NodeID, Incarnation, error) {
	return svcconsensus.Init(d, cluster, slot)
}

// Redraw draws a new id on an empty joining directory.
func Redraw(d DataDir) (NodeID, error) { return svcconsensus.Redraw(d) }

// Inspect reads a stopped node's directory without changing it.
func Inspect(d DataDir) (Report, error) { return svcconsensus.Inspect(d) }

// ClearQuarantine clears a quarantined or stopped directory by one of its
// two exits.
func ClearQuarantine(d DataDir, exit QuarantineExit, op Operator) error {
	return svcconsensus.ClearQuarantine(d, exit, op)
}

// Recover starts a new history from this node's committed log, every old
// voter left out attested fenced by every witness.
func Recover(d DataDir, newCluster ClusterID, members []Member, opt RecoverOptions) error {
	return svcconsensus.Recover(d, newCluster, members, opt)
}

// ReceiptMessage returns the bytes a witness signs to attest a fenced voter.
func ReceiptMessage(old, next ClusterID, voter NodeID) []byte {
	return svcconsensus.ReceiptMessage(old, next, voter)
}

// RestoreSnapshot starts a new history from a saved snapshot, offline.
func RestoreSnapshot(d DataDir, r io.Reader, newCluster ClusterID, members []Member, opt RestoreOptions) (SnapshotMeta, error) {
	return svcconsensus.RestoreSnapshot(d, r, newCluster, members, opt)
}

// NewTCPTransport builds the shipped mTLS transport; it is also a Joiner.
func NewTCPTransport(cfg TransportConfig) (*TCPTransport, error) {
	return svcconsensus.NewTCPTransport(cfg)
}

// NewTCPTransportFromPEM builds it from a certificate, a key and the
// consensus CA, as PEM files reloaded in place on renewal.
func NewTCPTransportFromPEM(certFile, keyFile, caFile string, cfg TransportConfig) (*TCPTransport, error) {
	return svcconsensus.NewTCPTransportFromPEM(certFile, keyFile, caFile, cfg)
}

// LoadPEMIdentity reads a node identity and the CA pool from PEM files.
func LoadPEMIdentity(certFile, keyFile, caFile string) (*PEMIdentity, *x509.CertPool, error) {
	return svcconsensus.LoadPEMIdentity(certFile, keyFile, caFile)
}

// SAN returns the canonical URI SAN a node certificate must carry.
func SAN(cluster ClusterID, slot string) *url.URL { return svcconsensus.SAN(cluster, slot) }

// SPKIOf returns a certificate's pin: the SHA-256 of its public key info.
func SPKIOf(c *x509.Certificate) [32]byte { return svcconsensus.SPKIOf(c) }

// The roles a Status reports.
const (
	RoleFollower     = coreconsensus.RoleFollower
	RolePreCandidate = coreconsensus.RolePreCandidate
	RoleCandidate    = coreconsensus.RoleCandidate
	RoleLeader       = coreconsensus.RoleLeader
	RoleLearner      = coreconsensus.RoleLearner
)

// The read modes and durabilities.
const (
	ReadIndex             = svcconsensus.ReadIndex
	ReadLease             = svcconsensus.ReadLease
	DurabilityVolatile    = svcconsensus.DurabilityVolatile
	DurabilityDurable     = svcconsensus.DurabilityDurable
	EntryCommand          = coreconsensus.EntryCommand
	LaneControl           = coreconsensus.LaneControl
	LaneData              = coreconsensus.LaneData
	LaneSnapshot          = coreconsensus.LaneSnapshot
	StampJoining          = coreconsensus.StampJoining
	StampMember           = coreconsensus.StampMember
	StampBootstrapPending = coreconsensus.StampBootstrapPending
	FlagTransfer          = coreconsensus.FlagTransfer
	FlagNoLease           = coreconsensus.FlagNoLease
	FlagNeedSnapshot      = coreconsensus.FlagNeedSnapshot
	MsgForward            = coreconsensus.MsgForward
	MsgApp                = coreconsensus.MsgApp
	MsgHeartbeat          = coreconsensus.MsgHeartbeat
	MsgVote               = coreconsensus.MsgVote
	MsgPreVote            = coreconsensus.MsgPreVote
	MsgTimeoutNow         = coreconsensus.MsgTimeoutNow
	MsgReadIndex          = coreconsensus.MsgReadIndex
	MsgForwardAdmin       = coreconsensus.MsgForwardAdmin
	MsgAppResp            = coreconsensus.MsgAppResp
	MsgVoteResp           = coreconsensus.MsgVoteResp
	MsgPreVoteResp        = coreconsensus.MsgPreVoteResp
	MsgHeartbeatResp      = coreconsensus.MsgHeartbeatResp
	MsgReadIndexResp      = coreconsensus.MsgReadIndexResp
	MsgForwardResp        = coreconsensus.MsgForwardResp
	MsgForwardAdminResp   = coreconsensus.MsgForwardAdminResp
)

// NewRaft opens a node over what Init and Bootstrap prepared. It never
// bootstraps.
func NewRaft(cfg Config) (*Node, error) { return svcconsensus.NewRaft(cfg) }

// Bootstrap founds a cluster on one founding node, with the IDENTICAL member
// list on every founder. Idempotent and resumable.
func Bootstrap(ctx context.Context, cfg Config, members []Member) error {
	return svcconsensus.Bootstrap(ctx, cfg, members)
}

// InitLog prepares a joining log on any LogStore: a random NodeID and a
// joining stamp. Init does the same on the shipped data directory.
func InitLog(log LogStore, cluster ClusterID, slot string) (NodeID, Incarnation, error) {
	return svcconsensus.InitLog(log, cluster, slot)
}

// RedrawLog draws a new id on an empty joining log.
func RedrawLog(log LogStore) (NodeID, error) { return svcconsensus.RedrawLog(log) }

// ProposeAs types the Apply result. A result of another dynamic type is
// RESULT_TYPE_MISMATCH carrying the committed token: the entry IS committed.
func ProposeAs[R any](ctx context.Context, r Replicated, data []byte) (Token, R, error) {
	return svcconsensus.ProposeAs[R](ctx, r, data)
}

// TimingDefaults are the default timings for the three-site RTT matrix.
func TimingDefaults() TimingContract { return svcconsensus.TimingDefaults() }

// Latest opens the newest complete snapshot of a store.
func Latest(s SnapshotStore) (SnapshotHandle, error) { return svcconsensus.Latest(s) }

// Lane reads the lane of an encoded frame from its frozen header alone.
func Lane(frame []byte) (FrameLane, error) { return wire.Lane(frame) }

// NewMemoryLog returns the in-memory LogStore double.
func NewMemoryLog() *MemoryLog { return svcconsensus.NewMemoryLog() }

// NewMemorySnapshots returns the in-memory SnapshotStore double.
func NewMemorySnapshots() *MemorySnapshots { return svcconsensus.NewMemorySnapshots() }

// NewMemoryNetwork returns an in-memory network of transports.
func NewMemoryNetwork() *MemoryNetwork { return svcconsensus.NewMemoryNetwork() }

// MemorySPKI is the key the memory transport authenticates a node with.
func MemorySPKI(id NodeID) [32]byte { return svcconsensus.MemorySPKI(id) }

// LeaderHint reads the known leader from a NOT_LEADER (R7-10).
func LeaderHint(err error) (NodeID, bool) { return svcconsensus.LeaderHint(err) }

// TokenOf reads the committed token an error carries (R7-10).
func TokenOf(err error) (Token, bool) { return svcconsensus.TokenOf(err) }

// The wire codecs are the service's, re-exported (R7-05).

// AppendMessage appends m's frame to dst.
func AppendMessage(dst []byte, m *Message) []byte { return wire.AppendMessage(dst, m) }

// ParseMessage decodes a frame: FRAME_INVALID, VERSION_UNSUPPORTED.
func ParseMessage(frame []byte, m *Message) error { return wire.ParseMessage(frame, m) }

// AppendSnapshotMeta appends the encoding of m.
func AppendSnapshotMeta(dst []byte, m *SnapshotMeta) []byte { return wire.AppendSnapshotMeta(dst, m) }

// ParseSnapshotMeta decodes a SnapshotMeta.
func ParseSnapshotMeta(b []byte, m *SnapshotMeta) error { return wire.ParseSnapshotMeta(b, m) }

// AppendToken appends the 41-byte encoding of t.
func AppendToken(dst []byte, t Token) []byte { return wire.AppendToken(dst, t) }

// ParseToken decodes a token.
func ParseToken(b []byte) (Token, error) { return wire.ParseToken(b) }

// The sentinels a caller matches with errors.Is or errs.HasCode.
var (
	ConsensusMisconfigured  = coreconsensus.ConsensusMisconfigured
	NotLeader               = coreconsensus.NotLeader
	OutcomeUnknown          = coreconsensus.OutcomeUnknown
	LeadershipUnconfirmed   = coreconsensus.LeadershipUnconfirmed
	MembershipChangePending = coreconsensus.MembershipChangePending
	MembershipInvalid       = coreconsensus.MembershipInvalid
	TransferFailed          = coreconsensus.TransferFailed
	EntryTooLarge           = coreconsensus.EntryTooLarge
	Compacted               = coreconsensus.Compacted
	StateMachineFailed      = coreconsensus.StateMachineFailed
	NodeStopped             = coreconsensus.NodeStopped
	EntrySuperseded         = coreconsensus.EntrySuperseded
	Overloaded              = coreconsensus.Overloaded
	VersionUnsupported      = coreconsensus.VersionUnsupported
	NodeIDRetired           = coreconsensus.NodeIDRetired
	NoSpace                 = coreconsensus.NoSpace
	ResultTypeMismatch      = coreconsensus.ResultTypeMismatch
	PayloadVersionRefused   = coreconsensus.PayloadVersionRefused
	LineageMismatch         = coreconsensus.LineageMismatch
	AdminPreconditionFailed = coreconsensus.AdminPreconditionFailed
	LogCorrupt              = coreconsensus.LogCorrupt
	HardStateCorrupt        = coreconsensus.HardStateCorrupt
	SnapshotCorrupt         = coreconsensus.SnapshotCorrupt
	StorageFailed           = coreconsensus.StorageFailed
	DataDirLocked           = coreconsensus.DataDirLocked
	DataDirMismatch         = coreconsensus.DataDirMismatch
	PeerRejected            = coreconsensus.PeerRejected
	ClusterMismatch         = coreconsensus.ClusterMismatch
	FrameInvalid            = coreconsensus.FrameInvalid
	SnapshotTooLarge        = coreconsensus.SnapshotTooLarge
	BootstrapRefused        = coreconsensus.BootstrapRefused
	TransportMisconfigured  = coreconsensus.TransportMisconfigured
	RecoverRefused          = coreconsensus.RecoverRefused
	LogQuarantined          = coreconsensus.LogQuarantined
	RestoreRefused          = coreconsensus.RestoreRefused
	DataDirInUse            = coreconsensus.DataDirInUse
	JoinRefused             = coreconsensus.JoinRefused
	StopMarkerPresent       = coreconsensus.StopMarkerPresent
)

// Compile-time proof that the engine implements the published ports.
var (
	_ Replicated          = (*Node)(nil)
	_ LeadershipWatcher   = (*Node)(nil)
	_ LogStore            = (*MemoryLog)(nil)
	_ SnapshotStore       = (*MemorySnapshots)(nil)
	_ Transport           = (*MemoryTransport)(nil)
	_ Joiner              = (*MemoryTransport)(nil)
	_ Transport           = (*TCPTransport)(nil)
	_ Joiner              = (*TCPTransport)(nil)
	_ UnreachableReporter = (*TCPTransport)(nil)
)
