package yaml

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// documentedNodeBound is the node bound the package documents (CLAUDE.md,
// limits_external_test.go), restated so a drifting constant fails here.
const documentedNodeBound int = 1 << 20

// Test_parser_add pins the node bound on the one function every node of a
// document is added through, at the constant itself: maxNodes nodes are
// accepted, each at the next index, and the node that would cross the bound
// is refused as UNMARSHAL_FAILED and not added.
//
// limits_external_test.go decodes documents of 2^20 nodes to prove the same
// bound end to end, which a coverage build cannot afford (see
// TestDocumentBounds). This test adds the nodes directly, so it costs the
// arena and not the scanner, and keeps the bound and its refusal inside the
// coverage run as well as the race suite.
func Test_parser_add(t *testing.T) {
	t.Parallel()
	//: the bound is the documented one.
	if maxNodes != documentedNodeBound {
		t.Fatalf("maxNodes = %d, documented as %d", maxNodes, documentedNodeBound)
	}
	p := &parser{}
	//: every node up to the bound is accepted, at the index it was given.
	for i := range maxNodes {
		index, err := p.add(node{line: 1})
		//: a refusal here would lower the bound every document meets.
		if err != nil || int(index) != i {
			t.Fatalf("node %d of %d: index %d, error %v", i+1, maxNodes, index, err)
		}
	}
	index, err := p.add(node{line: 1})
	//: the node past the bound is refused, under the decode failure's reason.
	if !errs.HasReason(err, "UNMARSHAL_FAILED") || index != noNode {
		t.Errorf("node %d: index %d, error %v, want noNode and UNMARSHAL_FAILED", maxNodes+1, index, err)
	}
	//: and it is not in the arena.
	if len(p.nodes) != maxNodes {
		t.Errorf("arena holds %d nodes after the refusal, want %d", len(p.nodes), maxNodes)
	}
}
