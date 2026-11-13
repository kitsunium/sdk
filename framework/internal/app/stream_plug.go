package app

import (
	"path/filepath"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's streams through the stream subsystem (issue #826, ADR 0179 step 4's
// shape): the core opens a streamed topic's log with a plug.StreamConfig and
// runs each of its subscriptions as a consumer group of the plug.Stream
// framework/app/stream plugs in; it imports none of the SDK's stream.

// streamPackage is the package a program imports for its streamed topics.
const streamPackage = "github.com/kitsunium/sdk/framework/app/stream"

// underStreams is what a streamed topic's stream is kept under in the data
// directory, beside its service's name and its own.
const underStreams string = "streams"

// streamTableService is what a stream's tables are named under, in place of
// a service: app_stream__<service>__<topic>.
const streamTableService string = frameworkName + "_stream"

// streamPartitions is how many partitions a streamed topic's stream has: a
// message's key chooses one, and the records of one key keep their order.
// It cannot change for the life of a stream.
const streamPartitions int = 8

// streameder is a node a streamed topic makes: the topic itself, and each
// of its subscriptions.
type streameder interface {
	streamed() bool
}

// openStreamWith opens the stream cfg places, through the stream subsystem:
// refused when the program does not import it — which the start says
// first, for every declaration at once (subsystem.go).
func (a *App) openStreamWith(cfg plug.StreamConfig) (plug.Stream, error) {
	d := plug.Streams.Load()
	if d == nil || d.Open == nil {
		return nil, streamsMissing()
	}
	cfg.Clock, cfg.Partitions, cfg.Wrap = pluggedClock{Timed: a.clock}, streamPartitions, a.wrapStream
	return d.Open(cfg)
}

// openTopicStream opens the stream of the streamed topic b: on the open
// database that keeps its service — a queue's rules, ADR 0178 C1 —, and the
// database it returns is where an append made in a transaction on it joins
// that transaction; else in the data directory, else in memory.
func (a *App) openTopicStream(b *nodeBase) (plug.Stream, *databaseRun, error) {
	cfg := plug.StreamConfig{Name: b.id}
	r := a.queueDatabase(b)
	if r == nil {
		if a.dataDir != "" {
			cfg.Dir = filepath.Join(a.dataDir, b.svc.name, underStreams, b.name)
		}
		s, err := a.openStreamWith(cfg)
		return s, nil, err
	}
	db := r.database()
	if db == nil {
		return nil, nil, Unavailable("database " + r.d.name + " is not open")
	}
	cfg.Database, cfg.Table = db, streamTable(b)
	s, err := a.openStreamWith(cfg)
	if err != nil {
		return nil, nil, err
	}
	return s, r, nil
}

// streamTable is the base name of the tables of the streamed topic b's
// stream on the database that keeps it: app_stream__<service>__<topic>.
func streamTable(b *nodeBase) string {
	return tableName(streamTableService, b.svc.name, "__"+b.name)
}

// streamsMissing is the refusal of what needs the stream subsystem in a
// program that does not import it.
func streamsMissing() error {
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"app: a streamed topic needs the stream subsystem, which the program does not import: "+streamPackage,
		errs.String("packages", streamPackage))
}

// streamsLinked reports whether the program imports framework/app/stream.
func streamsLinked() bool { return plug.Streams.Load() != nil }

// needsStream reports whether n needs the stream subsystem: a streamed
// topic, and each of its subscriptions.
func needsStream(n node) bool {
	s, ok := n.(streameder)
	return ok && s.streamed()
}
