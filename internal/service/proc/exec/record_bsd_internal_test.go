//go:build darwin || dragonfly || freebsd || netbsd || openbsd

// Package exec — the wait for a process record the kernel states valid
// (#1453): NetBSD answers the record of a process inside its execve(2) with
// p_uvalid 0 and no start. The records here are scripted, the pauses counted
// on a clock that never sleeps; the adoption tests hold a real child the test
// spawned.
package exec

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sleepCounter is a clock.Waiter whose Sleep returns at once and adds up what
// it was asked to wait; await calls nothing else.
type sleepCounter struct {
	clock.Waiter
	sleeps int
	slept  time.Duration
}

// Sleep counts d without waiting.
func (c *sleepCounter) Sleep(d time.Duration) {
	c.sleeps++
	c.slept += d
}

// scriptedRecords is a record source whose first invalid reads answer a
// record NetBSD gives during an execve — the users, no start, valid false —
// and every later one what then answers, and which counts its reads and
// probes.
type scriptedRecords struct {
	invalid       int
	then          func(pid int) (procRecord, error)
	alive         error
	reads, probes int
	clk           sleepCounter
}

// source returns the recordSource over s.
func (s *scriptedRecords) source() recordSource {
	return recordSource{
		read: func(pid int) (procRecord, error) {
			s.reads++
			if s.reads <= s.invalid {
				return procRecord{ruid: os.Getuid(), euid: os.Geteuid()}, nil
			}
			return s.then(pid)
		},
		alive: func(int) error {
			s.probes++
			return s.alive
		},
		clk: &s.clk,
	}
}

// TestRecordAwaitRereadsWithinTheBound pins await: a record not valid yet is
// read again every recordPoll on the injected clock, and returned once valid;
// one never valid within recordWait is ADOPTION_REFUSED with no record; a
// process the probe finds gone ends the wait with the probe's refusal, at
// once; a failed read is returned as it is, unprobed.
func TestRecordAwaitRereadsWithinTheBound(t *testing.T) {
	t.Parallel()
	const pid = 4242
	filled := procRecord{start: 1_700_000_000_123_456, ruid: 1001, euid: 1002, valid: true}
	never := int(recordWait/recordPoll) + 1
	type tc struct {
		name       string
		invalid    int
		then       func(int) (procRecord, error)
		alive      error
		want       procRecord
		wantCode   errs.Code
		wantESRCH  bool
		wantReads  int
		wantProbes int
		wantSlept  time.Duration
	}
	valid := func(int) (procRecord, error) { return filled, nil }
	gone := wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	cases := []tc{
		{name: "valid at once", then: valid, want: filled, wantReads: 1},
		{
			name: "valid after three reads", invalid: 3, then: valid, want: filled,
			wantReads: 4, wantProbes: 3, wantSlept: 3 * recordPoll,
		},
		{
			name: "never valid", invalid: never + 10, then: valid, wantCode: coreproc.CodeAdoptionRefused,
			wantReads: never, wantProbes: never, wantSlept: recordWait,
		},
		{
			name: "valid at the last read", invalid: never - 1, then: valid, want: filled,
			wantReads: never, wantProbes: never - 1, wantSlept: recordWait,
		},
		{
			name: "a zombie", invalid: 1, then: valid, alive: gone,
			wantCode: coreproc.CodeSignalFailed, wantESRCH: true, wantReads: 1, wantProbes: 1,
		},
		{
			name: "not to be watched", invalid: 2, then: valid,
			alive:    wrapHandleUnavailable(syscall.EACCES, errs.Int("pid", pid)),
			wantCode: coreproc.CodeHandleUnavailable, wantReads: 1, wantProbes: 1,
		},
		{
			name: "a failed read", then: func(int) (procRecord, error) { return procRecord{}, gone },
			wantCode: coreproc.CodeSignalFailed, wantESRCH: true, wantReads: 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &scriptedRecords{invalid: c.invalid, then: c.then, alive: c.alive}
		got, err := s.source().await(pid)
		if c.wantCode == 0 && err != nil {
			t.Fatalf("await = %v, want %+v", err, c.want)
		}
		if c.wantCode != 0 && (!errs.HasCode(err, c.wantCode) || c.wantESRCH != errors.Is(err, syscall.ESRCH)) {
			t.Fatalf("await = %+v, %v; want code %v (ESRCH %v)", got, err, c.wantCode, c.wantESRCH)
		}
		if got != c.want {
			t.Errorf("await = %+v, want %+v", got, c.want)
		}
		if s.reads != c.wantReads || s.probes != c.wantProbes || s.clk.slept != c.wantSlept ||
			time.Duration(s.clk.sleeps)*recordPoll != c.wantSlept {
			t.Errorf("%d reads, %d probes, %d pauses of %v in all; want %d, %d, %v",
				s.reads, s.probes, s.clk.sleeps, s.clk.slept, c.wantReads, c.wantProbes, c.wantSlept)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAttachRereadsARecordNotValidYet pins the adoption on a real child the
// test spawned, its records scripted as NetBSD answers them during an execve:
// a users bound adopts the child once the record read before the knotes, or
// the one read after them, turns valid; a record never valid within the bound
// refuses the child, ADOPTION_REFUSED, after recordWait of pauses, and leaves
// it alive.
func TestAttachRereadsARecordNotValidYet(t *testing.T) {
	t.Parallel()
	child := startRunningSleeper(t)
	own := coreproc.AttachValue{UIDs: []int{os.Getuid(), os.Geteuid()}}
	never := int(recordWait/recordPoll) + 1
	type tc struct {
		name      string
		invalid   int
		wantErr   bool
		wantSlept time.Duration
	}
	cases := []tc{
		{name: "the read before the knotes", invalid: 4, wantSlept: 4 * recordPoll},
		{name: "never valid", invalid: never, wantErr: true, wantSlept: recordWait},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &scriptedRecords{invalid: c.invalid, then: readRecord}
		src := s.source()
		src.alive = probeKnote
		p, err := src.attach(child.Pid, own)
		if c.wantErr {
			if p != nil || !errs.HasCode(err, coreproc.CodeAdoptionRefused) {
				t.Fatalf("attach = %v, %v, want ADOPTION_REFUSED", p, err)
			}
			if kErr := child.Signal(syscall.Signal(0)); kErr != nil {
				t.Errorf("the refused child is gone: %v", kErr)
			}
		} else {
			if err != nil || p == nil || p.PID() != child.Pid {
				t.Fatalf("attach = %v, %v, want the child", p, err)
			}
			p.(*adopted).release()
		}
		if s.clk.slept != c.wantSlept {
			t.Errorf("paused %v, want %v", s.clk.slept, c.wantSlept)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	// The read after the knotes: the first one, before them, is valid.
	reads := 0
	afterInvalid := recordSource{
		read: func(pid int) (procRecord, error) {
			reads++
			rec, err := readRecord(pid)
			if reads >= 2 && reads <= 4 {
				return procRecord{ruid: rec.ruid, euid: rec.euid}, err
			}
			return rec, err
		},
		alive: probeKnote,
		clk:   &sleepCounter{},
	}
	p, err := afterInvalid.attach(child.Pid, own)
	if err != nil || p == nil || p.PID() != child.Pid {
		t.Fatalf("attach with the read after the knotes not valid = %v, %v, want the child", p, err)
	}
	p.(*adopted).release()
	if reads != 5 {
		t.Errorf("%d reads, want 5: one before the knotes, four after", reads)
	}
}
