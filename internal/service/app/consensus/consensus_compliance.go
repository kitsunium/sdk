package consensus

import (
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The engine reaches every interface below by TYPE ASSERTION, on the ports it
// was handed: the data directory's hold, the stop markers, the silence
// condition, the log's gauges, the spool's release, the transport's frame
// bound and certificate expiry. An assertion cannot fail a build, so a
// shipped store or transport that stopped satisfying one would quietly lose
// the behaviour — a DataDir closed under a live Node, a start over a stop
// marker, a node voting through its silence. Each line below turns that
// into a compile error, here.
var (
	_ coreconsensus.LogStore      = (*fileLog)(nil)
	_ coreconsensus.SnapshotStore = (*fileSnapshots)(nil)
	_ coreconsensus.Transport     = (*TCPTransport)(nil)
	_ coreconsensus.Transport     = (*MemoryTransport)(nil)

	_ interface {
		compactPrefixAt(through, term uint64) error
	} = (*fileLog)(nil)
	_ interface {
		compactPrefixAt(through, term uint64) error
	} = (*MemoryLog)(nil)
	_ dirHolder                         = (*fileLog)(nil)
	_ interface{ checkMarkers() error } = (*fileLog)(nil)
	_ interface {
		silence() (term, index uint64, unreadable, err error)
	} = (*fileLog)(nil)
	_ interface{ endSilence() error } = (*fileLog)(nil)
	_ interface {
		logStats() (bytes, free, tails int64)
	} = (*fileLog)(nil)
	_ spaceReporter = (*fileLog)(nil)
	_ interface {
		markStopEach(errs.Code, string) (dirErr, runErr error)
	} = (*fileLog)(nil)
	_ interface{ release() } = (*fileSink)(nil)
	// A published snapshot's handle reads as a spool's file does.
	_ spoolFile = (*fileHandle)(nil)
	_ interface {
		Cluster() coreconsensus.ClusterID
	} = (*TCPTransport)(nil)
	_ interface{ maxFrameBytes() int } = (*TCPTransport)(nil)
	_ joinReleaser                     = (*TCPTransport)(nil)
	_ joinReleaser                     = (*MemoryTransport)(nil)
	_ refreshJoiner                    = (*TCPTransport)(nil)
	_ refreshJoiner                    = (*MemoryTransport)(nil)
	_ bindWarner                       = (*TCPTransport)(nil)
	_ bindWarner                       = (*MemoryTransport)(nil)
	// The transport hands the node an offer it could not decode, which
	// stops it when its leader made it (#665).
	_ undecodableBinder = (*TCPTransport)(nil)
	_ interface {
		CertExpiry() time.Time
		PeerCertExpiry() map[coreconsensus.NodeID]time.Time
		ReloadFailures() int
	} = (*TCPTransport)(nil)
)
