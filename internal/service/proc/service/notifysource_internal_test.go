package service

import (
	"os"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// scriptedListener returns its values, then its errors, in turn.
type scriptedListener struct {
	values []coreproc.NotificationValue
	errs   []error
}

func (l *scriptedListener) Recv() (coreproc.NotificationValue, error) {
	v, err := l.values[0], l.errs[0]
	l.values, l.errs = l.values[1:], l.errs[1:]
	return v, err
}

func (l *scriptedListener) Close() error { return nil }

// TestListenerSourceRefusalIsCounted pins #730 in the engine: a datagram the
// listener refuses comes back unverified — counted as refused — so a service
// sending garbage cannot end the reader by failing Recv sixty-four times;
// the socket's own failure still ends it.
func TestListenerSourceRefusalIsCounted(t *testing.T) {
	t.Parallel()
	src := &listenerSource{l: &scriptedListener{
		values: []coreproc.NotificationValue{{}, {}, {}},
		errs:   []error{coreproc.InvalidNotification, coreproc.CredentialMismatch, coreproc.ListenFailed},
	}}
	for i := range 2 {
		v, err := src.Recv(t.Context())
		if err != nil || v.Verified || v.PID != 0 {
			t.Errorf("refusal %d: %+v, %v, want an unverified message and no error", i, v, err)
		}
	}
	if _, err := src.Recv(t.Context()); err == nil {
		t.Errorf("a socket failure: nil error, want LISTEN_FAILED")
	}
}

// TestNotifyValueHandsOverDescriptors pins the mapping (S0d): the main
// process's pidfd first, then the stored descriptors — the engine's to keep
// or close —, MAINPIDFD=1's PID as MAINPID=, the sender's pidfd closed at
// once (no message carried it), and the kernel's user as the sender's.
func TestNotifyValueHandsOverDescriptors(t *testing.T) {
	t.Parallel()
	files := make([]*os.File, 3)
	for i := range files {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
		files[i] = r
		t.Cleanup(func() { _ = r.Close() })
	}
	v := coreproc.NotificationValue{
		State:   map[string]string{"MAINPIDFD": "1", "FDSTORE": "1"},
		MainPID: 4242,
		Descriptors: &coreproc.DescriptorsValue{
			Files: files[:1], MainPIDFD: files[1], SenderPIDFD: files[2],
		},
		SenderPID: 77,
		SenderUID: 0,
	}
	got := notifyValue(&v)
	if got.Fields["MAINPID"] != "4242" || len(got.Files) != 2 || !got.Verified || got.PID != 77 || got.UID != 0 {
		t.Fatalf("notifyValue = %+v", got)
	}
	if got.Files[0] != files[1] || got.Files[1] != files[0] {
		t.Errorf("Files = %v, want the pidfd %v first, then the stored %v", got.Files, files[1], files[0])
	}
	if _, err := files[2].Stat(); err == nil {
		t.Error("the sender's pidfd is still open")
	}
}
