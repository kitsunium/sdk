package rotfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// Test_rotatingSink_Rotate verifies on-demand rotation: an empty active file is
// a no-op while a non-empty one cuts a .1 backup and reopens an empty Path.
func Test_rotatingSink_Rotate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		payload    string
		wantBackup bool
	}{
		{name: "empty active file is a no-op", payload: "", wantBackup: false},
		{name: "non-empty file cuts a backup", payload: "payload\n", wantBackup: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "app.log")
			now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
			rs := newAgedSink(t, path, 0, now)
			//: a non-empty payload makes the file eligible for rotation.
			if tc.payload != "" {
				if _, werr := rs.Write(t.Context(), corelogger.RecordEvent{}, []byte(tc.payload)); werr != nil {
					t.Fatalf("Write: %v", werr)
				}
			}
			if rerr := rs.Rotate(); rerr != nil {
				t.Fatalf("Rotate: %v", rerr)
			}
			_, serr := os.Lstat(path + ".1")
			haveBackup := serr == nil
			//: a backup must exist iff the active file was non-empty.
			if haveBackup != tc.wantBackup {
				t.Fatalf("backup present = %v, want %v (stat err %v)", haveBackup, tc.wantBackup, serr)
			}
			//: the active Path must always exist and be empty after Rotate.
			fi, aerr := os.Stat(path)
			if aerr != nil {
				t.Fatalf("active missing after Rotate: %v", aerr)
			}
			if fi.Size() != 0 {
				t.Fatalf("active size = %d after Rotate, want 0", fi.Size())
			}
		})
	}
}
