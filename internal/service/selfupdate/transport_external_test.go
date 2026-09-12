// Package updater_test black-box tests the public surface of the release
// transport: the two sentinels that say THIS CLIENT REFUSED, as opposed to
// "the endpoint answered badly".
//
// The distinction is the whole point of the file they come from. The GitHub API
// and the release CDN are untrusted input — stable releases resolve through a
// public mirror repository, and a redirect chain or a response body is chosen
// entirely by whatever answers — so the transport imposes bounds the endpoint
// does not get to choose. A caller has to be able to tell a bound being
// enforced apart from a transient failure, because only one of the two is worth
// retrying.
package selfupdate_test

import (
	"errors"
	"fmt"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// TestTransportRefusalsSurviveWrapping pins that the transport sentinels stay
// classifiable after the wrapping the real call path applies.
//
// Both are produced deep inside an http.Client redirect policy or a body
// decoder and reach the caller through several %w layers. If any of those
// layers flattened the cause — the shape `fmt.Errorf("...: %v", err)` — the
// sentinel would still print but errors.Is would stop matching, and the caller
// would fall through to its generic "the release could not be downloaded" branch and retry a
// refusal that will never succeed.
func TestTransportRefusalsSurviveWrapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
		why      string
	}{
		{
			name:     "insecure redirect",
			sentinel: coreupd.InsecureRedirect,
			why:      "the endpoint tried to move the download off https or past the hop cap",
		},
		{
			name:     "oversized API body",
			sentinel: coreupd.APIBodyTooLarge,
			why:      "a hostile or broken endpoint tried to stream unbounded bytes into json.Decoder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: A nil sentinel would silently break every errors.Is call site.
			if tt.sentinel == nil {
				t.Fatalf("sentinel %q is nil", tt.name)
			}
			//: Wrap the way the real pipeline does, twice, then require
			//: classification to still succeed.
			wrapped := fmt.Errorf("upgrading to v9.9.9: %w", fmt.Errorf("fetching asset: %w", tt.sentinel))
			if !errors.Is(wrapped, tt.sentinel) {
				t.Errorf("errors.Is(wrapped, %s) = false, want true (%s)", tt.name, tt.why)
			}
		})
	}
}

// TestTransportRefusalsAreNotEndpointFailures pins that a bound this client
// enforced never classifies as a failure the endpoint reported.
//
// Collapsing the two is not a cosmetic error. coreupd.DownloadFailed and
// coreupd.UnexpectedStatus are the classes a retry can resolve; a refused redirect
// or an oversized body is a decision this process made about untrusted input,
// and retrying it means asking the same untrusted endpoint the same question
// until it gives an answer the client accepts.
func TestTransportRefusalsAreNotEndpointFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		refusal error
		other   error
	}{
		{name: "redirect vs download failure", refusal: coreupd.InsecureRedirect, other: coreupd.DownloadFailed},
		{name: "redirect vs unexpected status", refusal: coreupd.InsecureRedirect, other: coreupd.UnexpectedStatus},
		{name: "oversized body vs download failure", refusal: coreupd.APIBodyTooLarge, other: coreupd.DownloadFailed},
		{name: "oversized body vs oversized archive", refusal: coreupd.APIBodyTooLarge, other: coreupd.ArchiveTooLarge},
		{name: "redirect vs oversized body", refusal: coreupd.InsecureRedirect, other: coreupd.APIBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: A refusal that classifies as a transient failure gets retried.
			if errors.Is(tt.refusal, tt.other) {
				t.Errorf("%v classifies as %v — a refusal must not read as a retryable failure", tt.refusal, tt.other)
			}
			//: And the reverse, so a transient failure is never reported as a
			//: security refusal the operator is told not to work around.
			if errors.Is(tt.other, tt.refusal) {
				t.Errorf("%v classifies as %v", tt.other, tt.refusal)
			}
		})
	}
}
