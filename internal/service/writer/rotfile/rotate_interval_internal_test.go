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
