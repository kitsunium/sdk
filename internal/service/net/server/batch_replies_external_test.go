// Package server_test — BatchReplies, the opt-in batch of a datagram group's
// replies (ADR 0192).
package server_test

import (
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestBatchReplies pins the option's two sides: a datagram group honours it —
// a reply queued during its batch reaches the peer, nothing counted failed —
// as one without it does, replying at once; a stream group refuses it at Start
// with LoopOptionUnsupported naming it, and binds nothing.
func TestBatchReplies(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// opts are the datagram group's options.
		opts []server.GroupOption
	}
	tests := []tc{
		{name: "with it", opts: []server.GroupOption{server.BatchReplies()}},
		{name: "without it"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, addr := startUpper(t, c.opts...)

		for _, payload := range []string{"a", "bb", "ccc"} {
			if got := sendDatagram(t, addr, payload); got != strings.ToUpper(payload) {
				t.Fatalf("reply = %q, want %q", got, strings.ToUpper(payload))
			}
		}

		if f := srv.State().Datagrams.ReplyFailures; f != 0 {
			t.Fatalf("ReplyFailures = %d, want 0", f)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBatchReplies_streamGroup pins the refusal: a stream group has no
// datagram replies to batch, so Start refuses it by name (R6), binding nothing.
func TestBatchReplies_streamGroup(t *testing.T) {
	t.Parallel()
	srv := server.New()
	t.Cleanup(func() { closeOrFail(t, srv) })
	srv.Group("g", server.BatchReplies(), server.Listen("tcp", "127.0.0.1:0")).Handle(echoConnHandler{})

	err := srv.Start(t.Context())

	if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) || !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
		return f.Key() == "option" && f.StringValue() == "BatchReplies"
	}) {
		t.Fatalf("Start = %v, want LoopOptionUnsupported naming BatchReplies", err)
	}
	if st := srv.State(); len(st.Listeners) != 0 {
		t.Fatalf("a refused Start left %d listeners", len(st.Listeners))
	}
}
