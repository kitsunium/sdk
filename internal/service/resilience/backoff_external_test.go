// Package resilience_test — the public backoff as a caller computes it.
package resilience_test

import (
	"testing"
	"time"

	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestBackoffValueIsTheKernelCurve pins that the published curve IS the kernel
// one, not a copy that could drift from it: a value built here is a
// kernel/backoff.Value with no conversion, and both spellings compute the same
// waits. The curve's own behaviour — growth, ceiling, the negative-wrap fix,
// the jitter band — is pinned where it lives, in internal/kernel/backoff.
func TestBackoffValueIsTheKernelCurve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		attempt int
		want    time.Duration
	}
	published := svcres.BackoffValue{BaseDelay: time.Second, MaxDelay: time.Minute}
	//: takes the KERNEL type, and is handed the published one with no
	//: conversion — which compiles only because the alias is the type.
	ownedDelay := func(curve kbackoff.Value, attempt int) time.Duration { return curve.Delay(attempt) }
	tests := []tc{
		{"the first failure waits the base", 1, time.Second},
		{"the sixth is thirty-two times the base", 6, 32 * time.Second},
		{"far past the ceiling it stays there", 1000, time.Minute},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := published.Delay(c.attempt); got != c.want {
			t.Errorf("BackoffValue.Delay(%d) = %v, want %v", c.attempt, got, c.want)
		}
		if got := ownedDelay(published, c.attempt); got != c.want {
			t.Errorf("kernel Value.Delay(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
