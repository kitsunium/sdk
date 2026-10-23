package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// `otlp migrate` (issue #580): the batches the OTLP export left in its file
// queues under <data>/.otlp when otlp-database moved its queues onto a
// database — which nothing sends from then on, and which the start reports
// (otlp.file-queue-left) — moved onto that database: each signal's waiting
// and dead batches written into its table as they lie, through the queue
// subsystem's ports that read and write a message as it lies, in one
// transaction per signal, and declared done only once the table holds every
// one of them — by fingerprints, as `data copy` declares a queue copied
// (datacopy_queue.go), but as a part of what the table holds, since the
// export may already have queued its own batches there. The file queue is
// then moved aside under <data>/.data-copy/<instant>/, so the start no
// longer reports it. It may run while the product runs: a batch it moves is
// sent by the running export at its next poll, and counted pending by it
// (the forwarder's recount). Never a route (D13).

// The export's queue policy, as otlpengine.openQueue opens its queues: a
// lease above two attempts, deliveries no outage reaches, a batch at most
// the export's byte bound, up to 64 MiB.
const (
	otlpLeaseMargin     time.Duration = 30 * time.Second
	otlpMaxDeliveries   int           = 1 << 30
	otlpMaxQueueMessage int64         = 64 << 20
)

// otlpQueuePolicy is the policy the export opens its queues under.
func otlpQueuePolicy(cfg *plug.OTLPConfig) plug.QueuePolicyValue {
	return plug.QueuePolicyValue{
		VisibilityTimeout: 2*cfg.SendTimeout + otlpLeaseMargin,
		MaxDeliveries:     otlpMaxDeliveries,
		MaxMessageBytes:   int(min(cfg.MaxBytes, otlpMaxQueueMessage)),
	}
}

// migrateOTLP moves the batches of signals the export left under the data
// directory onto the database otlp-database names; refused when it names
// none.
func (a *App) migrateOTLP(ctx context.Context, signals []string, stdout io.Writer) error {
	if resolvedSetting(a, a.opts.otlp.database) == "" {
		return failure(CodeOTLPExport, "OTLP_MIGRATE_NO_DATABASE",
			"otlp migrate moves the export's batches onto the database otlp-database names, and it names none", nil)
	}
	return a.withOTLPQueues(ctx, func(cfg *plug.OTLPConfig) error {
		for _, signal := range signals {
			if err := a.migrateOTLPSignal(ctx, cfg, signal, stdout); err != nil {
				return err
			}
		}
		return nil
	})
}

// migrateOTLPSignal moves the batches of signal's file queue onto its table,
// and says how many on stdout; nothing when the file queue holds none.
func (a *App) migrateOTLPSignal(ctx context.Context, cfg *plug.OTLPConfig, signal string, stdout io.Writer) error {
	dir := otlpFileQueue(a.dataDir, signal)
	if a.dataDir == "" || !fileQueueHolds(dir) {
		fmt.Fprintf(stdout, "%s: no batch left under the data directory\n", signal)
		return nil
	}
	p, err := a.otlpQueuePart(cfg, signal, dir)
	if err != nil {
		return err
	}
	n := 0
	err = cfg.Database.Transact(ctx, func(ctx context.Context) error {
		var copyErr error
		if n, copyErr = p.copy(ctx); copyErr != nil {
			return copyErr
		}
		return p.verifyHeld(ctx)
	})
	if err != nil {
		return errors.Join(err, p.closeFrom())
	}
	aside := filepath.Join(a.dataDir, dataCopyDir, a.clock.Now().UTC().Format("20060102T150405Z"))
	if err := errors.Join(p.closeFrom(), moveAside(a.dataDir, dir, aside)); err != nil {
		return failure(CodeOTLPExport, "OTLP_MIGRATE_MOVE",
			"the batches are on the database, but their file queue could not be moved aside: move it by hand, or a second migrate copies them again",
			err, errs.String("signal", signal), errs.String("dir", dir))
	}
	fmt.Fprintf(stdout, "%s: %d batches moved onto %s, fingerprints found there; the file queue moved aside to %s\n", signal, n, p.name, aside)
	return nil
}

// otlpQueuePart opens signal's file queue at dir and its table on the
// export's database, each as the export opens it, through the queue
// subsystem.
func (a *App) otlpQueuePart(cfg *plug.OTLPConfig, signal, dir string) (queuePart, error) {
	policy := otlpQueuePolicy(cfg)
	name := "otlp/" + signal
	src, err := a.openQueueWith(plug.QueueConfig{Name: name, Dir: dir, Policy: policy})
	if err != nil {
		return queuePart{}, failure(CodeOTLPExport, "OTLP_QUEUE", "the OTLP export's queue cannot be opened", err, errs.String("signal", signal))
	}
	closeFrom := func() error { return nil }
	if c, ok := src.(io.Closer); ok {
		closeFrom = sync.OnceValue(c.Close)
	}
	from, ok := src.(copyQueue)
	if !ok {
		return queuePart{}, failure(CodeOTLPExport, "OTLP_QUEUE", "a file queue cannot be read as it lies", closeFrom(), errs.String("signal", signal))
	}
	table := cfg.Tables[signal]
	dst, err := a.openQueueWith(plug.QueueConfig{Name: name, Database: cfg.Database, Table: table, Policy: policy})
	if err != nil {
		return queuePart{}, errors.Join(failure(CodeOTLPExport, "OTLP_QUEUE", "the OTLP export's queue cannot be opened", err, errs.String("signal", signal)), closeFrom())
	}
	to, ok := dst.(copyQueue)
	if !ok {
		return queuePart{}, failure(CodeOTLPExport, "OTLP_QUEUE", "a queue's table cannot be written as a message lies", closeFrom(), errs.String("signal", signal))
	}
	return queuePart{from: from, to: to, closeFrom: closeFrom, dir: dir, name: table}, nil
}

// verifyHeld reports whether the table holds every message of the file
// queue, as many times as the file queue does, by fingerprints: the table
// may hold others besides, which the export queued there itself.
func (p *queuePart) verifyHeld(ctx context.Context) error {
	src, err := messageDigests(ctx, p.from)
	if err != nil {
		return err
	}
	dst, err := messageDigests(ctx, p.to)
	if err != nil {
		return err
	}
	found := 0
	for _, d := range dst {
		if found < len(src) && d == src[found] {
			found++
		}
	}
	if found < len(src) {
		return failure(CodeOTLPExport, "OTLP_MIGRATE_DIVERGED", "a batch moved is not found on the database", nil,
			errs.String("table", p.name), errs.Int("source", len(src)), errs.Int("found", found))
	}
	return nil
}
