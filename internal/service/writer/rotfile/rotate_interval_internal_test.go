package rotfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_rotatingSink_tickRotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		mode    string // "prune" (idle + aged sibling) | "rotate" | "break"
		wantErr bool
	}
	tests := []tc{
		{name: "idle tick prunes aged sibling", mode: "prune"},
		{name: "non-empty tick rotates", mode: "rotate"},
		{name: "rotate failure is stashed", mode: "break", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "app.log")
		now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
		s := newSink(t, Config{Path: path, MaxBytes: 4, MaxAgeDays: 3, Clock: &fakeClock{now: now}})
		//: closeQuiet tolerates the broken-descriptor arm's already-closed file.
		defer closeQuiet(t, s)
		//: the prune arm seeds a sibling older than the 3-day cutoff.
		if c.mode == "prune" {
			seedBackup(t, path, 1, now.AddDate(0, 0, -10))
		}
		//: the rotate/break arms need a non-empty active file (size > 0).
		if c.mode == "rotate" || c.mode == "break" {
			//: an 8-byte write lands (size==0 guard) and leaves size > 0.
			if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("12345678")); werr != nil {
				t.Fatalf("%s: seed write: %v", c.name, werr)
			}
		}
		//: the break arm closes the descriptor so the tick's rotate close fails.
		if c.mode == "break" {
			//: pre-close forces rotate()'s first Close to error.
			if cerr := s.f.Close(); cerr != nil {
				t.Fatalf("%s: pre-close: %v", c.name, cerr)
			}
		}
		//: drive one interval tick.
		s.tickRotate()
		//: failure arm — the typed rotate error must be stashed for the next Write.
		if c.wantErr {
			//: the stash must carry the rotate-failed code.
			if !errs.HasCode(s.tickErr, CodeRotFileRotateFailed) {
				t.Errorf("%s: tickErr=%v want rotate-failed", c.name, s.tickErr)
			}
			return
		}
		//: success arms never stash an error.
		if s.tickErr != nil {
			t.Errorf("%s: unexpected tickErr=%v", c.name, s.tickErr)
		}
		//: the prune arm must have expired the aged sibling.
		if c.mode == "prune" {
			//: the aged .1 sibling must be gone after the idle tick.
			if _, serr := os.Lstat(path + ".1"); !os.IsNotExist(serr) {
				t.Errorf("%s: aged backup still present (stat %v)", c.name, serr)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rotatingSink_tickErr_writesRecord asserts that a Write following a
// failed interval tick still persists its record (the tick failure must not
// cost a log line — F6-tickdrop) and reports the failure via Config.OnError
// exactly once.
func Test_rotatingSink_tickErr_writesRecord(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{name: "failed tick still writes record and fires OnError once"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "app.log")
		//: capture the one-shot OnError signal without blocking the write path.
		var seen error
		calls := 0
		s := newSink(t, Config{Path: path, OnError: func(err error) {
			//: record the surfaced tick error and how many times it fired.
			seen = err
			calls++
		}})
		defer closeQuiet(t, s)
		//: stash a typed rotate failure as if a tick had failed on the daemon.
		s.mu.Lock()
		s.tickErr = RotFileRotateFailed
		s.mu.Unlock()
		//: the next Write must succeed AND persist its record despite the stash.
		rec := []byte("after-tick-failure\n")
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, rec); werr != nil {
			t.Fatalf("Write after failed tick: unexpected error %v", werr)
		}
		//: the stash must be cleared so subsequent writes are unaffected.
		if s.tickErr != nil {
			t.Errorf("tickErr not cleared after Write: %v", s.tickErr)
		}
		//: OnError must have fired exactly once with the rotate-failed code.
		if calls != 1 || !errs.HasCode(seen, CodeRotFileRotateFailed) {
			t.Errorf("OnError fired %d times with err=%v; want 1 rotate-failed", calls, seen)
		}
		//: the record must be on disk — the triggering line was not dropped.
		got, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read back log: %v", rerr)
		}
		if string(got) != string(rec) {
			t.Errorf("log contents = %q, want %q (record dropped on tick-error path)", got, rec)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
