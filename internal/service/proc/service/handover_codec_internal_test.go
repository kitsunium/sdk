package service

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The hand-over's wire form (S0h, kitsunium/sdk#738): a model written down is
// read back whole; a record a later image added is skipped (N to N-1), one an
// earlier image did not write is its zero (N-1 to N); another version, a
// corrupt or cut payload, a value this image does not define are refused.

// fullModel is a model with every field the hand-over carries set: S0d's
// store and key, S0e's bus level and revalidation, S0g's group flags,
// svcwire's sandbox failure, the timers, the counters.
func fullModel() model {
	at := time.Unix(1_700_000_000, 123_456_789)
	m := model{
		changed: at, limitBegin: at.Add(-time.Second), runtimeAt: at.Add(time.Hour), revalidateBegin: at.Add(time.Minute),
		status: "up", busError: "org.x.Err", varlinkError: "io.x.Err", exitStatusText: "bye", pidFileCause: "PID_FILE_REFUSED",
		job:      job{kind: jobRestart, stopped: true, waiters: []uint64{7}},
		pending:  job{kind: jobReload},
		store:    []storeEntry{{name: "state", id: 3, poll: true}, {name: "sock", id: 5}},
		storeKey: "store/abc", sandboxCause: coreproc.CodeSandboxFailed,
		sandboxFields: []errs.FieldValue{errs.String("step", "mounts"), errs.String("errno", "EPERM")},
		mainExit: coreproc.ExitValue{
			Code: 3, Signal: 9, Signaled: true, UserTime: time.Millisecond, SystemTime: 2 * time.Millisecond, MaxRSS: 4096, CoreDumped: true,
			Usage: &coreproc.UsageValue{MinorFaults: 1, MajorFaults: 2, InBlock: 3, OutBlock: 4, VoluntaryCtxSw: 5, InvoluntaryCtxSw: 6},
		},
		seq: [timerCount]uint64{4, 5, 6}, generation: 41, notifyRefused: 2, reloadBegin: 99,
		storeSeq: 5, storeRefused: 1, storeDiscarded: 2, watchdogPeriod: 3 * time.Second,
		mainPID: 4242, controlPID: 4343, cmd: 1, errno: 13, limitCount: 2, restarts: 3,
		state: coresvc.StateAutoRestart, sub: coresvc.SubAutoRestart, result: coresvc.ResultExitCode,
		reloadResult: coresvc.ResultTimeout, notifyAccess: coresvc.NotifyAccessMain, notify: notifyReady,
		list: listStart, mainFlags: coresvc.FlagIgnoreFailure,
		flags: flagMainExited | flagCgroupGood | flagGroupPopulated | flagPrepare | flagBusOwned | flagRan,
	}
	m.deadlines = [timerCount]time.Time{at.Add(time.Second), at.Add(2 * time.Second), at.Add(3 * time.Second)}
	m.published.At = at.Add(-time.Minute)
	return m
}

// wantModel is m as it reads back: the waiters dropped — they were the
// outgoing image's callers —, times without their monotonic reading.
func wantModel(m model) model {
	m.job.waiters, m.pending.waiters, m.closeWaiters = nil, nil, nil
	return m
}

// TestHandoverModelRoundTrip: every field written is read back.
func TestHandoverModelRoundTrip(t *testing.T) {
	m := fullModel()
	got, err := decodeModel(encodeModel(&m))
	if err != nil {
		t.Fatalf("decodeModel: %v", err)
	}
	if want := wantModel(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("the model read back differs:\n got %+v\nwant %+v", got, want)
	}
	var zero model
	if got, err = decodeModel(encodeModel(&zero)); err != nil || !reflect.DeepEqual(got, zero) {
		t.Fatalf("a zero model read back %+v, %v", got, err)
	}
}

// withoutRecord drops the first top-level record tagged tag from b.
func withoutRecord(t *testing.T, b []byte, tag uint16) []byte {
	t.Helper()
	r := &reader{b: b}
	out := &writer{}
	dropped := false
	for k, v, ok := r.next(); ok; k, v, ok = r.next() {
		if k == tag && !dropped {
			dropped = true
			continue
		}
		out.record(k, v)
	}
	if !dropped || r.err != nil {
		t.Fatalf("no record %d to drop (%v)", tag, r.err)
	}
	return out.b
}

// TestHandoverVersionsWithin: within one version an image reads what an
// older one wrote — a record it did not write is the zero — and what a newer
// one wrote — a record it does not know is skipped.
func TestHandoverVersionsWithin(t *testing.T) {
	m := fullModel()
	b := encodeModel(&m)
	older := withoutRecord(t, b, mRevalidateBegin)
	got, err := decodeModel(older)
	want := wantModel(m)
	want.revalidateBegin = time.Time{}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("N-1 to N: %v\n got %+v\nwant %+v", err, got, want)
	}
	w := &writer{b: append([]byte(nil), b...)}
	w.record(0x7FFF, []byte("a field a later image added"))
	w.u64(0x7FFE, 7)
	if got, err = decodeModel(w.b); err != nil || !reflect.DeepEqual(got, wantModel(m)) {
		t.Fatalf("N to N-1: %v\n got %+v", err, got)
	}
}

// wantInvalid requires HANDOVER_INVALID whose why field is why.
func wantInvalid(t *testing.T, err error, why string) {
	t.Helper()
	if !errs.HasCode(err, coresvc.CodeHandoverInvalid) {
		t.Fatalf("%q: %v, want HANDOVER_INVALID", why, err)
	}
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "why" && f.StringValue() == why {
			return
		}
	}
	t.Fatalf("%v: why is not %q (fields %v)", err, why, errs.FieldsOf(err))
}

// TestHandoverPayloadRefused: a payload of another version — a later one,
// or none —, a corrupt body, a cut payload, another magic are refused.
func TestHandoverPayloadRefused(t *testing.T) {
	body := []byte{}
	w := &writer{b: body}
	w.str(uName, "u")
	ok := seal(w.b, handoverVersion)
	if got, err := unseal(ok); err != nil || string(got) != string(w.b) {
		t.Fatalf("a sound payload: %v", err)
	}
	_, err := unseal(seal(w.b, handoverVersion+1))
	wantInvalid(t, err, "a version this image does not read")
	_, err = unseal(seal(w.b, 0))
	wantInvalid(t, err, "a version this image does not read")
	corrupt := append([]byte(nil), ok...)
	corrupt[payloadHeader] ^= 0xFF
	_, err = unseal(corrupt)
	wantInvalid(t, err, "a body that does not match its sum")
	_, err = unseal(ok[:len(ok)-1])
	wantInvalid(t, err, "a body length that does not match")
	_, err = unseal(ok[:10])
	wantInvalid(t, err, "a payload of no possible length")
	magic := append([]byte(nil), ok...)
	magic[0] = 'X'
	_, err = unseal(magic)
	wantInvalid(t, err, "no hand-over magic")
	long := append([]byte(nil), ok...)
	binary.LittleEndian.PutUint32(long[10:], uint32(len(w.b)+1))
	_, err = unseal(long)
	wantInvalid(t, err, "a body length that does not match")
}

// TestHandoverModelRefused: a record cut short, an integer of the wrong
// width, a value or a flag this image does not define, a store whose ids do
// not hold are refused.
func TestHandoverModelRefused(t *testing.T) {
	m := fullModel()
	b := encodeModel(&m)
	_, err := decodeModel(b[:len(b)-3])
	wantInvalid(t, err, "a record past the payload's end")
	_, err = decodeModel(append(append([]byte(nil), b...), 1, 0))
	wantInvalid(t, err, "a record cut short")
	w := &writer{}
	w.record(mState, []byte{1, 2})
	_, err = decodeModel(w.b)
	wantInvalid(t, err, "an integer of the wrong width")
	cases := []func(*model){
		func(m *model) { m.state = 200 },
		func(m *model) { m.sub = 200 },
		func(m *model) { m.result = 200 },
		func(m *model) { m.notifyAccess = 9 },
	}
	for _, change := range cases {
		bad := fullModel()
		change(&bad)
		_, err = decodeModel(encodeModel(&bad))
		wantInvalid(t, err, "a value this image does not define")
	}
	steps := []func(*model){
		func(m *model) { m.list = listCount },
		func(m *model) { m.job.kind = jobReload + 1 },
		func(m *model) { m.notify = notifyStopping + 1 },
	}
	for _, change := range steps {
		bad := fullModel()
		change(&bad)
		_, err = decodeModel(encodeModel(&bad))
		wantInvalid(t, err, "a step this image does not define")
	}
	flags := &writer{b: encodeModel(&m)}
	flags.u64(mFlags, uint64(flagClosing))
	_, err = decodeModel(flags.b)
	wantInvalid(t, err, "a step this image does not define")
	for _, store := range [][]storeEntry{{{id: 3}, {id: 3}}, {{id: 0}}, {{id: 9}}} {
		bad := fullModel()
		bad.store = store
		_, err = decodeModel(encodeModel(&bad))
		wantInvalid(t, err, "a store whose ids do not hold")
	}
}

// TestHandoverFlagsFrozen: Close's and the end's flags are never written: a
// closing unit is not handed over, and a stray bit cannot ride along.
func TestHandoverFlagsFrozen(t *testing.T) {
	m := fullModel()
	m.flags |= flagClosing | flagExited
	got, err := decodeModel(encodeModel(&m))
	if err != nil || got.has(flagClosing) || got.has(flagExited) || !got.has(flagBusOwned) {
		t.Fatalf("flags read back %b, %v", got.flags, err)
	}
}
