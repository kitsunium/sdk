package rotfile_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/writer/rotfile"
)

// openRot opens the "rotfile" writer through the public registry and registers
// cleanup. Shared black-box driver so each case stays focused on assertions.
func openRot(t *testing.T, cfg rotfile.Config) corelogger.Sink {
	t.Helper()
	//: resolve via the registry so the blank-import registration is exercised.
	sink, err := writer.Open("rotfile", cfg)
	//: a clean open is the precondition for the behaviour cases.
	if err != nil {
		t.Fatalf("open %+v: %v", cfg, err)
	}
	t.Cleanup(func() {
		//: surface a cleanup close failure rather than discarding it.
		if cerr := sink.Close(); cerr != nil {
			t.Errorf("cleanup close: %v", cerr)
		}
	})
	return sink
}

// writeN sends count payloads of payloadLen bytes through sink in order so the
// rotation threshold trips deterministically. Not a test — a sequential driver.
func writeN(t *testing.T, sink corelogger.Sink, count, payloadLen int) {
	t.Helper()
	//: a fixed-byte payload makes the size math deterministic across writes.
	p := make([]byte, payloadLen)
	//: drive count writes so callers cross MaxBytes a known number of times.
	for range count {
		//: each write may rotate; surface the first failure.
		if _, werr := sink.Write(t.Context(), corelogger.RecordEvent{}, p); werr != nil {
			t.Fatalf("write failed: %v", werr)
		}
	}
}

func TestRotFileRegistered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"importing the package registers the rotfile writer"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the package-level var must have self-registered the factory.
		if !writer.Name("rotfile").Known() {
			t.Errorf("rotfile writer not registered after import")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRotFileOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"valid path opens", rotfile.Config{Path: filepath.Join(dir, "a.log")}, false},
		{"per-writer floor opens", rotfile.Config{Path: filepath.Join(dir, "b.log"), MinLevel: level.Warn}, false},
		{"empty path rejected", rotfile.Config{Path: ""}, true},
		{"wrong config type rejected", writer.ConsoleConfig{}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("rotfile", c.cfg)
		//: clean up any descriptor the happy path opened.
		if sink != nil {
			t.Cleanup(func() {
				//: surface a close failure rather than discarding it.
				if cerr := sink.Close(); cerr != nil {
					t.Errorf("%s: cleanup close: %v", c.name, cerr)
				}
			})
		}
		//: failure arm — error + nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: happy arm — nil error + usable sink.
		if err != nil || sink == nil {
			t.Errorf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRotFileRotatesAtMaxBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a write past MaxBytes renames the active file to .1"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "rot.log")
		sink := openRot(t, rotfile.Config{Path: path, MaxBytes: 10})
		//: two 8-byte writes: the first fills, the second trips the 10-byte cap.
		writeN(t, sink, 2, 8)
		//: the backup must exist after the threshold-crossing write.
		if _, serr := os.Stat(path + ".1"); serr != nil {
			t.Errorf("expected rotated backup %s.1: %v", path, serr)
		}
		fi, aerr := os.Stat(path)
		//: the reopen must have recreated Path.
		if aerr != nil {
			t.Fatalf("active file missing after rotate: %v", aerr)
		}
		//: only the post-rotation write should be in the fresh active file.
		if fi.Size() != 8 {
			t.Errorf("active size=%d want 8 (one post-rotation record)", fi.Size())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRotFileMaxBackupsEviction(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"MaxBackups caps the retained .N siblings"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "cap.log")
		sink := openRot(t, rotfile.Config{Path: path, MaxBytes: 5, MaxBackups: 2})
		//: many threshold-crossing writes force several rotations.
		writeN(t, sink, 6, 8)
		//: the oldest slot beyond the cap must have been evicted.
		if _, serr := os.Stat(path + ".3"); !os.IsNotExist(serr) {
			t.Errorf("expected %s.3 evicted, stat err=%v", path, serr)
		}
		//: the newest retained backup must survive.
		if _, serr := os.Stat(path + ".2"); serr != nil {
			t.Errorf("expected %s.2 retained: %v", path, serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRotFileCompressProduces0600Gz(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"Compress writes a 0600 .gz on rotation"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "z.log")
		sink := openRot(t, rotfile.Config{Path: path, MaxBytes: 5, Compress: true})
		//: two writes trip the cap once and produce a compressed backup.
		writeN(t, sink, 2, 8)
		fi, serr := os.Stat(path + ".1.gz")
		//: the .gz must exist after a compressing rotation.
		if serr != nil {
			t.Fatalf("expected %s.1.gz: %v", path, serr)
		}
		//: the gzip sibling must be 0600, never gzip's default perms.
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("gz perm=%o want 600", perm)
		}
		//: the uncompressed .1 must not linger alongside the .gz.
		if _, perr := os.Stat(path + ".1"); !os.IsNotExist(perr) {
			t.Errorf("expected uncompressed %s.1 removed, stat err=%v", path, perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// writeRecords drives count distinct payloads through the production Write path
// and returns the exact bytes handed to each call so the caller can reconcile
// them against what actually landed on the real filesystem. Not a test — a
// deterministic end-to-end driver over the live registry sink.
func writeRecords(t *testing.T, sink corelogger.Sink, payloads [][]byte) {
	t.Helper()
	//: each Write may rotate; a failure aborts the end-to-end reconciliation.
	for _, p := range payloads {
		if _, werr := sink.Write(t.Context(), corelogger.RecordEvent{}, p); werr != nil {
			t.Fatalf("write %q: %v", p, werr)
		}
	}
}

// readGzip decompresses the gzip artefact at path and returns its plaintext, so
// a compressing rotation can be reconciled against the original record bytes.
func readGzip(t *testing.T, path string) []byte {
	t.Helper()
	raw, rerr := os.ReadFile(path)
	//: the .gz must be readable before it can be decompressed.
	if rerr != nil {
		t.Fatalf("read %s: %v", path, rerr)
	}
	zr, zerr := gzip.NewReader(bytes.NewReader(raw))
	//: a malformed gzip header means the compaction wrote garbage.
	if zerr != nil {
		t.Fatalf("gzip reader %s: %v", path, zerr)
	}
	got, derr := io.ReadAll(zr)
	//: the trailer must decompress cleanly for the bytes to be trustworthy.
	if derr != nil {
		t.Fatalf("gunzip %s: %v", path, derr)
	}
	return got
}

// TestRotFileEndToEndBytesLandOnDisk is the real-I/O end-to-end: it drives the
// production registry sink across a size-triggered rotation and asserts the
// exact record bytes landed where rotation routes them — the pre-rotation record
// in the .1 backup, the post-rotation record in the freshly reopened active
// file — by reading the real files back, not by trusting return values.
func TestRotFileEndToEndBytesLandOnDisk(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"rotation routes each record to its real on-disk destination"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "e2e.log")
		//: MaxBytes=6 so an 8-byte first record fills, then the second rotates.
		sink := openRot(t, rotfile.Config{Path: path, MaxBytes: 6})
		first, second := []byte("AAAAAAAA"), []byte("BBBB")
		//: drive both records through the live Write path in order.
		writeRecords(t, sink, [][]byte{first, second})
		backup, berr := os.ReadFile(path + ".1")
		//: the pre-rotation record must be exactly what the .1 backup holds.
		if berr != nil || !bytes.Equal(backup, first) {
			t.Fatalf("backup=%q err=%v want %q", backup, berr, first)
		}
		active, aerr := os.ReadFile(path)
		//: only the post-rotation record may live in the reopened active file.
		if aerr != nil || !bytes.Equal(active, second) {
			t.Fatalf("active=%q err=%v want %q", active, aerr, second)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRotFileEndToEndCompressedBytesRoundTrip drives the production sink with
// Compress on and asserts the rotated record survives as a real, decompressible
// gzip artefact whose plaintext equals the original bytes — proving the
// compaction is lossless end-to-end, not merely that a .gz file appeared.
func TestRotFileEndToEndCompressedBytesRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a compressing rotation preserves the record bytes through gzip"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "e2ez.log")
		//: MaxBytes=6 trips one compressing rotation after the first record.
		sink := openRot(t, rotfile.Config{Path: path, MaxBytes: 6, Compress: true})
		first, second := []byte("payload!!"), []byte("tail")
		//: drive both records so the first is rotated and gzipped.
		writeRecords(t, sink, [][]byte{first, second})
		//: the rotated record must decompress back to the exact original bytes.
		if got := readGzip(t, path+".1.gz"); !bytes.Equal(got, first) {
			t.Errorf("gunzip=%q want %q", got, first)
		}
		active, aerr := os.ReadFile(path)
		//: the post-rotation record must live uncompressed in the active file.
		if aerr != nil || !bytes.Equal(active, second) {
			t.Errorf("active=%q err=%v want %q", active, aerr, second)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRotFileSymlinkRefused(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"symlink path refused with RotFileOpenFailed"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		dir := t.TempDir()
		link := filepath.Join(dir, "link.log")
		//: plant a symlink at the configured Path so the open must refuse it.
		if lerr := os.Symlink(filepath.Join(dir, "real.log"), link); lerr != nil {
			t.Fatalf("symlink: %v", lerr)
		}
		sink, err := writer.Open("rotfile", rotfile.Config{Path: link})
		//: a symlink target must be refused before any descriptor is created.
		if sink != nil {
			t.Cleanup(func() {
				//: surface a close failure rather than discarding it.
				if cerr := sink.Close(); cerr != nil {
					t.Errorf("cleanup close: %v", cerr)
				}
			})
			t.Fatalf("opened a symlink target")
		}
		//: the refusal must carry the documented open code.
		if !errs.HasCode(err, rotfile.CodeRotFileOpenFailed) {
			t.Errorf("err=%v want CodeRotFileOpenFailed", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
