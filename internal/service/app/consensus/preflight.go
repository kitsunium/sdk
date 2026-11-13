package consensus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// Preflight's defaults and bounds (ADR 0152 D16): 256 syncs of a 4 KiB
// record, a record about the size of a small batch; the scratch file never
// grows past one segment of the default size.
const (
	preflightSyncs       int = 256
	preflightRecordBytes int = 4096
	preflightMaxSyncs    int = 1 << 16
	// preflightReserve is Config's default LogReserveBytes.
	preflightReserve int64 = 256 << 20
	// preflightElectionFactor is the CLAUDE.md timing rule: a campaign is
	// two durable writes, and an ElectionTimeoutMin not above three times
	// the fdatasync p99 stayed leaderless through 41 terms (#598).
	preflightElectionFactor time.Duration = 3
)

// The verdicts' fields and rules, spelled as Config spells the field.
const (
	verdictElectionField = "InitialTiming.ElectionTimeoutMin"
	verdictElectionRule  = "not above three times the fdatasync p99: a campaign is two durable writes, and elections over slower syncs stayed leaderless (#598)"
	verdictReserveField  = "LogReserveBytes"
	verdictReserveRule   = "the filesystem's free bytes are under the log's reserve: the no-space alarm rises at once"
	verdictFlockField    = "DataDir"
	verdictFlockRule     = "no native flock(2) on this platform: NewDataDir refuses UNSUPPORTED_PLATFORM (ADR 0144)"
	verdictUnackedField  = "Transport"
	verdictUnackedRule   = "no kernel bound on unacknowledged bytes on this platform: a dead stream is closed by the read-silence bound alone (#1116)"
)

// PreflightOptions tunes Preflight: how much it writes, the timing contract
// it judges the measurement against, and where its time and its verdicts
// go.
type PreflightOptions struct {
	// Syncs is the number of records written, each synced; 0 is 256. It is
	// refused negative or above 65536.
	Syncs int
	// RecordBytes is the size of one record; 0 is 4096. It is refused
	// negative, and Syncs records of it above DefaultSegmentBytes.
	RecordBytes int
	// Timing is the contract judged against the measurement; the zero value
	// judges nothing (ADR 0031).
	Timing coreconsensus.TimingContractSpec
	// LogReserveBytes is the reserve the free space is judged against; 0 is
	// Config's default, 256 MiB.
	LogReserveBytes int64
	// Clock times each sync; nil is clock.System.
	Clock clock.Timed
	// Logger receives each verdict at Warn; nil logs nothing.
	Logger corelogger.Logger
}

// PreflightValue is what Preflight measured: the latency of one fdatasync of
// a record, at the median, the 99th percentile (nearest rank) and its
// highest, the free bytes of the filesystem, what the platform offers, and
// a verdict per rule the measurement or the platform breaks. A sync faster
// than the clock's resolution reads 0: on Windows, whose clock advances in
// steps, a fast disk's median may be 0 while its tail is not. The verdicts
// read the 99th percentile and the free bytes, never the median.
type PreflightValue struct {
	FsyncP50, FsyncP99, FsyncMax time.Duration
	// FreeBytes is what an unprivileged writer may still use there, -1 when unknown.
	FreeBytes int64
	// Flock is false where the platform has no flock(2), and NewDataDir refuses (ADR 0144).
	Flock bool
	// UserTimeout is false where the transport sets no kernel bound on
	// unacknowledged bytes (the BSDs, illumos, Solaris): a dead stream is
	// closed by the read-silence bound alone (#1116).
	UserTimeout bool
	// Verdicts lists each rule broken, empty when none is.
	Verdicts []PreflightVerdictValue
}

// PreflightVerdictValue is one rule Preflight found broken: the field it judges,
// spelled as Config spells it, and the rule.
type PreflightVerdictValue struct {
	Field, Rule string
}

// Preflight measures, on a stopped node, the disk the log of dir would trust
// (D16): it writes opts.Syncs records of opts.RecordBytes, each followed by
// the log's own fdatasync, into a scratch file it creates, zero-filled at
// their total size like a segment, in dir's log directory (dir itself when
// there is none yet, named preflight-*.tmp, which the log's recovery
// deletes should a crash leave one), then removes it: the directory holds
// after it what it held before. dir must exist. Where dir holds a LOCK,
// Preflight takes it as NewDataDir does, so a directory a live node holds
// is refused DATA_DIR_LOCKED; where the platform has no flock(2) (illumos,
// Solaris) it takes none and reports Flock false. It refuses nothing on
// what it measures: the verdicts are returned, and logged at Warn to
// opts.Logger, for the product to decide (D17). A failed write or sync is
// STORAGE_FAILED, options out of their bounds CONSENSUS_MISCONFIGURED.
func Preflight(dir string, opts PreflightOptions) (PreflightValue, error) {
	return preflight(dir, &opts, datasync)
}

// preflight is Preflight with the sync it times, which a test replaces to
// drive the measurement on a ManualClock.
func preflight(dir string, opts *PreflightOptions, sync func(*os.File) error) (measured PreflightValue, err error) {
	if err := completePreflight(opts); err != nil {
		return PreflightValue{}, err
	}
	if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
		if serr != nil && !errors.Is(serr, os.ErrNotExist) {
			return PreflightValue{}, storageFailed("stat", serr)
		}
		return PreflightValue{}, misconfigured("dir", "not an existing directory")
	}
	unlock, err := preflightLock(dir)
	if err != nil {
		return PreflightValue{}, err
	}
	defer func() {
		if uerr := unlock(); uerr != nil && err == nil {
			err = uerr
		}
	}()
	lat, err := measureSyncs(scratchDir(dir), opts, sync)
	if err != nil {
		return PreflightValue{}, err
	}
	measured = PreflightValue{
		FreeBytes: fsFree(dir), Flock: flock.Native, UserTimeout: unackedBound,
	}
	measured.FsyncP50, measured.FsyncP99, measured.FsyncMax = nearestRank(lat, 50), nearestRank(lat, 99), lat[len(lat)-1]
	measured.Verdicts = judgePreflight(&measured, opts)
	warnVerdicts(opts.Logger, &measured)
	return measured, nil
}

// completePreflight gives opts its defaults and refuses its bounds.
func completePreflight(opts *PreflightOptions) error {
	if err := checkPreflightBounds(opts); err != nil {
		return err
	}
	if opts.Syncs == 0 {
		opts.Syncs = preflightSyncs
	}
	if opts.RecordBytes == 0 {
		opts.RecordBytes = preflightRecordBytes
	}
	if opts.LogReserveBytes == 0 {
		opts.LogReserveBytes = preflightReserve
	}
	if int64(opts.Syncs)*int64(opts.RecordBytes) > DefaultSegmentBytes {
		return misconfigured("PreflightOptions.RecordBytes", "Syncs records of it above DefaultSegmentBytes")
	}
	if opts.Clock == nil {
		opts.Clock = clock.System
	}
	return nil
}

// checkPreflightBounds refuses the options a default cannot repair: a
// negative size or a count above preflightMaxSyncs.
func checkPreflightBounds(opts *PreflightOptions) error {
	switch {
	case opts.Syncs < 0 || opts.Syncs > preflightMaxSyncs:
		return misconfigured("PreflightOptions.Syncs", "negative or above 65536")
	case opts.RecordBytes < 0:
		return misconfigured("PreflightOptions.RecordBytes", "negative")
	case opts.LogReserveBytes < 0:
		return misconfigured("PreflightOptions.LogReserveBytes", "negative")
	}
	return nil
}

// preflightLock takes dir's LOCK as NewDataDir does, refusing
// DATA_DIR_LOCKED while another holder has it, and returns its release.
// A directory with no LOCK was never opened by a node, and none can hold
// it: nothing is taken, and nothing is created there. No lock is taken
// where the platform has none (ADR 0144).
func preflightLock(dir string) (release func() error, err error) {
	none := func() error { return nil }
	if !flock.Native {
		return none, nil
	}
	lf, err := os.OpenFile(filepath.Join(dir, fileLock), os.O_RDWR, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return none, nil
	case err != nil:
		return nil, storageFailed("lock", err)
	}
	// The file stays open while the lock is held, and release closes it; a
	// refusal below has no release to hand out, so this defer closes it.
	defer func() {
		if release == nil {
			err = alsoFailed(err, "close", lf.Close())
		}
	}()
	held, err := flock.TryLock(lf)
	if err != nil {
		return nil, storageFailed("lock", err)
	}
	if !held {
		return nil, origin(coreconsensus.DataDirLocked, errs.String("dir", dir))
	}
	return func() error {
		err := flock.Unlock(lf)
		if err != nil {
			err = storageFailed("unlock", err)
		}
		if cerr := lf.Close(); cerr != nil && err == nil {
			err = storageFailed("close lock", cerr)
		}
		return err
	}, nil
}

// scratchDir is where the scratch file goes: the log directory of dir when
// it exists, the disk the log trusts, else dir itself.
func scratchDir(dir string) string {
	logDir := filepath.Join(dir, "log")
	if fi, err := os.Stat(logDir); err == nil && fi.IsDir() {
		return logDir
	}
	return dir
}

// measureSyncs writes opts.Syncs records into a fresh scratch file in dir,
// zero-filled and synced first as a segment is, each record followed by
// sync, and returns the latencies, sorted. The file is removed whatever
// happened; a failed removal is a storage failure too, since it would leave
// a file behind.
func measureSyncs(dir string, opts *PreflightOptions, sync func(*os.File) error) (lat []time.Duration, err error) {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	path := filepath.Join(dir, "preflight-"+hex.EncodeToString(nonce[:])+".tmp")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, storageFailed("preflight create", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = storageFailed("preflight close", cerr)
		}
		if rerr := os.Remove(path); rerr != nil && err == nil {
			err = storageFailed("preflight remove", rerr)
		}
	}()
	rec := make([]byte, opts.RecordBytes)
	if err := zeroFill(f, rec, opts.Syncs); err != nil {
		return nil, err
	}
	for i := range rec {
		rec[i] = byte(i) | 1
	}
	return timeSyncs(f, rec, opts, sync)
}

// zeroFill writes n zero records of len(rec) into f and syncs it, as a
// segment is prepared, so the timed writes below overwrite allocated blocks.
func zeroFill(f segFile, rec []byte, n int) error {
	for i := range n {
		if _, err := f.WriteAt(rec, int64(i)*int64(len(rec))); err != nil {
			return storageFailed("preflight zero-fill", err)
		}
	}
	if err := f.Sync(); err != nil {
		return storageFailed("preflight zero-fill", err)
	}
	return nil
}

// timeSyncs overwrites each of opts.Syncs records of f with rec, times the
// sync that follows each, and returns the latencies, sorted.
func timeSyncs(f *os.File, rec []byte, opts *PreflightOptions, sync func(*os.File) error) ([]time.Duration, error) {
	lat := make([]time.Duration, opts.Syncs)
	for i := range opts.Syncs {
		if _, err := f.WriteAt(rec, int64(i)*int64(len(rec))); err != nil {
			return nil, storageFailed("write", err)
		}
		start := opts.Clock.Now()
		if err := sync(f); err != nil {
			return nil, storageFailed("fdatasync", err)
		}
		lat[i] = opts.Clock.Since(start)
	}
	slices.Sort(lat)
	return lat, nil
}

// nearestRank is the p-th percentile of sorted by nearest rank.
func nearestRank(sorted []time.Duration, p int) time.Duration {
	r := (p*len(sorted) + 99) / 100
	return sorted[max(r, 1)-1]
}

// judgePreflight lists the rules v breaks.
func judgePreflight(v *PreflightValue, opts *PreflightOptions) []PreflightVerdictValue {
	var out []PreflightVerdictValue
	if opts.Timing != (coreconsensus.TimingContractSpec{}) &&
		opts.Timing.ElectionTimeoutMin <= preflightElectionFactor*v.FsyncP99 {
		out = append(out, PreflightVerdictValue{Field: verdictElectionField, Rule: verdictElectionRule})
	}
	if v.FreeBytes >= 0 && v.FreeBytes < opts.LogReserveBytes {
		out = append(out, PreflightVerdictValue{Field: verdictReserveField, Rule: verdictReserveRule})
	}
	if !v.Flock {
		out = append(out, PreflightVerdictValue{Field: verdictFlockField, Rule: verdictFlockRule})
	}
	if !v.UserTimeout {
		out = append(out, PreflightVerdictValue{Field: verdictUnackedField, Rule: verdictUnackedRule})
	}
	return out
}

// warnVerdicts logs each verdict at Warn, with the measurement it rests on.
func warnVerdicts(log corelogger.Logger, v *PreflightValue) {
	ctx := context.Background()
	if log == nil || len(v.Verdicts) == 0 || !log.Enabled(ctx, level.Warn) {
		return
	}
	for _, vd := range v.Verdicts {
		log.Log(ctx, level.Warn, "consensus preflight: "+vd.Field+" "+vd.Rule,
			lstr("field", vd.Field), lstr("fsync_p99", v.FsyncP99.String()),
			lstr("free_bytes", strconv.FormatInt(v.FreeBytes, 10)))
	}
}
