package kit_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
)

// The heap carries no label: its samples go to the node whose code is on
// their stack.
//
// The heap profile is sampled — about one allocation per 512 KiB, scaled
// back up — so the eight megabytes Hoard holds may read as five or ten, and
// did read 5.8 once: the test asks where the bytes are, not how many exactly.
func TestHeapProfileFindsTheNodeByItsCode(t *testing.T) {
	app := startBench(t)
	if r := call(t, app, "POST /hoard", noBody); r.status != http.StatusNoContent {
		t.Fatalf("hoard: %d %s", r.status, r.body)
	}
	r := call(t, app, "GET /_kit/api/profile/heap", noBody)
	if r.status != http.StatusOK {
		t.Fatalf("heap: %d %s", r.status, r.body)
	}
	var p model.Profile
	r.json(t, &p)
	if p.Kind != model.ProfileHeap || p.Unit != "bytes" || p.Total < 3<<20 {
		t.Fatalf("heap profile %+v", p)
	}
	var hoarded float64
	for _, n := range p.Nodes {
		if n.Node == "bench/endpoint/Hoard" {
			hoarded = n.Value
		}
	}
	if hoarded < 3<<20 || hoarded > p.Total {
		t.Fatalf("the hoarder holds %.0f bytes of %.0f: %+v", hoarded, p.Total, p.Nodes)
	}
}

// Goroutines are grouped by the node they work for and the loop they
// belong to, with the runtime's own word for what they wait on.
func TestGoroutinesAreGroupedByNodeAndLoop(t *testing.T) {
	app := startBench(t)
	waiting := make(chan response, 3)
	for range 3 {
		go func() { waiting <- call(t, app, "GET /wait", noBody) }()
	}
	defer func() {
		openGate()
		for range 3 {
			<-waiting
		}
	}()
	check := func(t *testing.T) {
		var g model.Goroutines
		eventually(t, "three waiting handlers", func() bool {
			// A fresh value each poll: decoding into the last one would keep
			// the members this answer omits.
			g = model.Goroutines{}
			call(t, app, "GET /_kit/api/goroutines", noBody).json(t, &g)
			for _, grp := range g.Groups {
				if grp.Node == "bench/endpoint/Wait" && grp.Count == 3 {
					return true
				}
			}
			return false
		})
		var wait, accept *model.GoroutineGroup
		for i := range g.Groups {
			switch grp := &g.Groups[i]; {
			case grp.Node == "bench/endpoint/Wait":
				wait = grp
			case grp.Loop == "http" && grp.State == "IO wait" && slices.ContainsFunc(grp.Stack, func(f string) bool { return strings.HasSuffix(f, ".Accept") }):
				// The SDK engine's accept loop, blocked in the listener.
				accept = grp
			}
		}
		if wait.State != "select" || !strings.HasSuffix(wait.Top, ".Wait") || wait.Loop != "http" || len(wait.Stack) == 0 {
			t.Errorf("the waiting handlers: %+v", wait)
		}
		if accept == nil || accept.State != "IO wait" {
			t.Errorf("the accept loop: %+v", accept)
		}
		total := 0
		for _, grp := range g.Groups {
			total += grp.Count
		}
		if g.Total < 10 || total != g.Total {
			t.Errorf("total %d, groups add up to %d", g.Total, total)
		}
	}
	// Each case sets the traceback's labels itself: once GODEBUG changes at
	// run time, the runtime reads an unset tracebacklabels as 0, not as its
	// default.
	t.Run("labels in the traceback", func(t *testing.T) {
		t.Setenv("GODEBUG", "tracebacklabels=1")
		check(t)
	})
	t.Run("labels matched from the profile", func(t *testing.T) {
		t.Setenv("GODEBUG", "tracebacklabels=0")
		check(t)
	})
}
