package server

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// errTaken is the refusal the fake binds of Test_bindRetrying wait out.
var errTaken = errors.New("address in use")

// Test_bindRetrying pins the bounded rebind: an address still held is bound
// again after rebindDelay, at most attempts times, the wait reported; any
// other refusal, or the last attempt's, is returned at once; a first-attempt
// bind reports no wait; a cancelled context ends the wait.
func Test_bindRetrying(t *testing.T) {
	t.Parallel()
	inUse := func(err error) bool { return errors.Is(err, errTaken) }
	binder := func(fails int, err error) (func() (int, error), *int) {
		calls := 0
		return func() (int, error) {
			calls++
			if calls <= fails {
				return 0, err
			}
			return 42, nil
		}, &calls
	}
	//: bound at the first attempt: no wait.
	bind, calls := binder(0, nil)
	if v, wait, err := bindRetrying(t.Context(), 4, inUse, bind); v != 42 || wait != 0 || err != nil || *calls != 1 {
		t.Fatalf("first attempt: %d %v %v, %d calls", v, wait, err, *calls)
	}
	//: held twice, then free.
	bind, calls = binder(2, errTaken)
	if v, wait, err := bindRetrying(t.Context(), 4, inUse, bind); v != 42 || err != nil || *calls != 3 || wait < rebindDelay(1)+rebindDelay(2) {
		t.Fatalf("rebind: %d %v %v, %d calls", v, wait, err, *calls)
	}
	//: still held at the last attempt.
	bind, calls = binder(9, errTaken)
	if _, _, err := bindRetrying(t.Context(), 3, inUse, bind); !errors.Is(err, errTaken) || *calls != 3 {
		t.Fatalf("exhausted: %v, %d calls", err, *calls)
	}
	//: any other refusal is not waited out.
	other := errors.New("permission denied")
	bind, calls = binder(9, other)
	if _, wait, err := bindRetrying(t.Context(), 8, inUse, bind); !errors.Is(err, other) || *calls != 1 || wait != 0 {
		t.Fatalf("other refusal: %v %v, %d calls", err, wait, *calls)
	}
	//: the caller gives up: the refusal stands, before the next attempt.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	bind, calls = binder(9, errTaken)
	if _, _, err := bindRetrying(ctx, 8, inUse, bind); !errors.Is(err, errTaken) || *calls != 1 {
		t.Fatalf("cancelled: %v, %d calls", err, *calls)
	}
}

// Test_exclusiveApplies pins which sockets take SO_EXCLUSIVEADDRUSE: a
// unicast TCP or UDP one on Windows, never a multicast or AF_UNIX one, and
// none on a platform without the option.
func Test_exclusiveApplies(t *testing.T) {
	t.Parallel()
	windows := runtime.GOOS == "windows"
	for _, c := range []struct {
		network, address string
		want             bool
	}{
		{"tcp4", "127.0.0.1:80", windows},
		{"tcp6", "[::1]:80", windows},
		{"tcp", "0.0.0.0:0", windows},
		{"udp4", "0.0.0.0:53", windows},
		{"udp4", "239.1.2.3:5000", false},
		{"udp6", "[ff02::1]:5000", false},
		{"unix", `C:\tmp\s.sock`, false},
	} {
		if got := exclusiveApplies(c.network, c.address); got != c.want {
			t.Errorf("%s %s: %t, want %t", c.network, c.address, got, c.want)
		}
	}
}
