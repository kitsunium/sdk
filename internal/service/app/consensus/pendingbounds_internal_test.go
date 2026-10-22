package consensus

import (
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestHuntR8ProvePendingBelowEntryValidation is #557: a MaxPendingBytes
// below MaxEntryBytes was accepted, and every entry between the two passed
// Propose's size gate only to be refused OVERLOADED by the pending budget,
// even on an idle cluster, forever: a caller retrying on OVERLOADED burnt
// its whole deadline. The configuration is now refused at NewNode, like an
// entry bound above the batch bound.
func TestHuntR8ProvePendingBelowEntryValidation(t *testing.T) {
	type boundsCase struct {
		name    string
		set     func(*Config)
		refused bool
	}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 7}}
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	c.nodes = []*testNode{tn}
	for _, tc := range []boundsCase{
		{"pending below the default entry bound", func(cfg *Config) { cfg.MaxPendingBytes = 4096 }, true},
		{"pending one byte below the entry bound", func(cfg *Config) { cfg.MaxEntryBytes, cfg.MaxPendingBytes = 4096, 4095 }, true},
		{"pending equal to the entry bound", func(cfg *Config) { cfg.MaxEntryBytes, cfg.MaxPendingBytes = 4096, 4096 }, false},
		{"defaults", func(*Config) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := c.config(tn)
			tc.set(&cfg)
			_, err := withDefaults(&cfg)
			if got := errs.HasCode(err, coreconsensus.CodeConsensusMisconfigured); got != tc.refused {
				t.Fatalf("withDefaults with MaxEntryBytes=%d MaxPendingBytes=%d: %v, refused=%v want %v",
					cfg.MaxEntryBytes, cfg.MaxPendingBytes, err, got, tc.refused)
			}
		})
	}
}
