// Package config — the shared sentinel-wrapping helper.
package config

import (
	"errors"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapAs pins the origin-wins decision every fallible path here depends on.
//
// The sentinel has to stay the origin so its code survives, because a cause is
// frequently an *errs.Error from another package — a codec, a file source
// someone else wrote — and letting it become the origin would hand the caller a
// code from a domain they never called into.
func Test_wrapAs(t *testing.T) {
	t.Parallel()
	//: a cause that is itself typed is the case origin-wins exists for.
	foreign := kerrs.Define(kerrs.Code(0x00_03_1F_01), "FOREIGN_REASON",
		"a foreign public message", "a foreign private message")

	type tc struct {
		name         string
		sentinel     *kerrs.Error
		cause        error
		wantHasCause bool
	}
	tests := []tc{
		{"a source failure with no cause", coreconfig.ConfigSourceFailed, nil, false},
		{"a source failure with a plain cause", coreconfig.ConfigSourceFailed, errors.New("read failed"), true},
		{"a source failure with a typed cause", coreconfig.ConfigSourceFailed, foreign, true},
		{"a decode failure", coreconfig.ConfigDecodeFailed, errors.New("bad json"), true},
		{"a validation failure", coreconfig.ConfigValidationFailed, errors.New("port required"), true},
		{"a watch failure", coreconfig.ConfigWatchFailed, errors.New("poll interval must be > 0"), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := wrapAs(c.sentinel, c.cause)
		if got == nil {
			t.Fatal("wrapAs returned nil")
		}

		//: the sentinel is the origin: its code survives even a typed cause.
		wantCode, _ := kerrs.CodeOf(c.sentinel)
		if !kerrs.HasCode(got, wantCode) {
			t.Errorf("wrapAs = %v, want code %v", got, wantCode)
		}
		//: and a caller must be able to match the sentinel itself.
		if !errors.Is(got, c.sentinel) {
			t.Errorf("errors.Is(err, sentinel) = false for %v", got)
		}

		hasCause := false
		for _, f := range kerrs.FieldsOf(got) {
			if f.Key() == "cause" {
				hasCause = true
			}
		}
		//: the cause message rides as a field so it stays diagnosable without
		//: becoming the origin.
		if hasCause != c.wantHasCause {
			t.Errorf("a cause field is present = %v, want %v", hasCause, c.wantHasCause)
		}
		//: a nil cause returns the bare sentinel, which is what lets a caller
		//: compare it by identity.
		if c.cause == nil && got != error(c.sentinel) {
			t.Error("wrapAs with a nil cause did not return the bare sentinel")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_withCause pins the refusals this package states itself: the sentinel
// stays the typed origin and the sentence rides in the same `cause` field
// wrapAs fills. The poll watcher's two input guards are the callers — their
// sentence must survive the move off the stdlib errors rule 2 bans.
func Test_withCause(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		got       error
		wantCode  kerrs.Code
		wantCause string
	}
	tests := []tc{
		{
			"a stated refusal", withCause(coreconfig.ConfigSourceFailed, "a sentence"),
			coreconfig.CodeConfigSourceFailed, "a sentence",
		},
		{
			"a non-positive poll interval", pollWatcher{interval: 0}.validateInputs(func() {}),
			coreconfig.CodeConfigWatchFailed, causeNonPositiveInterval,
		},
		{
			"a nil onChange", pollWatcher{interval: time.Second}.validateInputs(nil),
			coreconfig.CodeConfigWatchFailed, causeNilOnChange,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !kerrs.HasCode(c.got, c.wantCode) {
			t.Fatalf("%s = %v, want code %v", c.name, c.got, c.wantCode)
		}
		cause := ""
		for _, f := range kerrs.FieldsOf(c.got) {
			if f.Key() == "cause" {
				cause = f.StringValue()
			}
		}
		if cause != c.wantCause {
			t.Errorf("%s: cause field = %q, want %q", c.name, cause, c.wantCause)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
