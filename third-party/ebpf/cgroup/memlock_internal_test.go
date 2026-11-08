package cgroup

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// TestNeedsMemlockBefore511 reads releases of distribution kernels on both
// sides of 5.11, and refuses to guess on one it cannot read.
func TestNeedsMemlockBefore511(t *testing.T) {
	cases := map[string]bool{
		"6.12.101+deb13-amd64":    false,
		"5.11.0":                  false,
		"5.15.0-122-generic":      false,
		"5.10.0-28-cloud-amd64":   true,
		"5.4.0-200-generic":       true,
		"4.18.0-553.el8_10.86_64": true,
		"4.19":                    true,
		"5":                       true,
		"":                        true,
		"x.y.z":                   true,
	}
	//: each release.
	for release, want := range cases {
		//: the documented answer.
		if got := needsMemlock(release); got != want {
			t.Errorf("needsMemlock(%q) = %v, want %v", release, got, want)
		}
	}
}

// TestMemlockIsRaisedOnceBeforeAnOldKernelsFirstLoad simulates a 5.10
// kernel: concurrent first loads raise the limit exactly once, and the
// answer is every caller's.
func TestMemlockIsRaisedOnceBeforeAnOldKernelsFirstLoad(t *testing.T) {
	var raised atomic.Int32
	m := &memlocker{
		release: func() (string, error) { return "5.10.0-28-cloud-amd64", nil },
		raise:   func() error { raised.Add(1); return nil },
	}
	var wg sync.WaitGroup
	//: sixteen first loads at once.
	for range 16 {
		wg.Go(func() {
			//: every caller is told the limit is raised.
			if err := m.remove(); err != nil {
				t.Errorf("remove: %v", err)
			}
		})
	}
	wg.Wait()
	//: once.
	if n := raised.Load(); n != 1 {
		t.Fatalf("raised %d times, want 1", n)
	}
}

// TestMemlockIsLeftAloneFrom511 simulates a 6.12 kernel: nothing is raised.
func TestMemlockIsLeftAloneFrom511(t *testing.T) {
	m := &memlocker{
		release: func() (string, error) { return "6.12.101+deb13-amd64", nil },
		raise:   func() error { t.Fatal("raised on 6.12"); return nil },
	}
	//: no error and no raise.
	if err := m.remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

// TestMemlockRaisedWhenTheReleaseIsUnknown simulates a uname that fails:
// the limit is raised, as on an old kernel.
func TestMemlockRaisedWhenTheReleaseIsUnknown(t *testing.T) {
	var raised bool
	m := &memlocker{
		release: func() (string, error) { return "", errors.New("uname refused") },
		raise:   func() error { raised = true; return nil },
	}
	//: raised, no error.
	if err := m.remove(); err != nil || !raised {
		t.Fatalf("remove = %v, raised = %v", err, raised)
	}
}

// TestMemlockRefusedIsLoadRefused simulates a 5.10 kernel without
// CAP_SYS_RESOURCE: the refusal is LOAD_REFUSED, its cause in the chain,
// and it is the answer of every later call too.
func TestMemlockRefusedIsLoadRefused(t *testing.T) {
	cause := errors.New("operation not permitted")
	m := &memlocker{
		release: func() (string, error) { return "5.4.0-200-generic", nil },
		raise:   func() error { return cause },
	}
	err := m.remove()
	//: typed, the cause kept.
	if !errs.HasCode(err, corebpf.CodeLoadRefused) || !errors.Is(err, cause) {
		t.Fatalf("remove = %v, want LOAD_REFUSED over the cause", err)
	}
	//: the same answer, without a second raise.
	if again := m.remove(); again != err {
		t.Fatalf("second remove = %v, want the first answer", again)
	}
}
