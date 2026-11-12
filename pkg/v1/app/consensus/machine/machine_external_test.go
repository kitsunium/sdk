package machine_test

import (
	"fmt"

	"github.com/kitsunium/sdk/pkg/v1/app/consensus"
	"github.com/kitsunium/sdk/pkg/v1/app/consensus/machine"
)

// ExampleKV applies three commands the way a node applies the committed
// ones, in log order: a swap that takes an absent key, a put, and a swap
// on the revision the first one read, which the put moved past.
func ExampleKV() {
	kv := machine.NewKV()
	apply := func(cmd []byte) machine.KVResult {
		return kv.Apply(consensus.EntryValue{Kind: consensus.EntryCommand, Data: cmd}).(machine.KVResult)
	}
	took := apply(machine.KVCompareAndSwap("leader", []byte("fra"), 0))
	fmt.Println(took.Applied, took.Revision)
	moved := apply(machine.KVPut("leader", []byte("par")))
	fmt.Println(moved.Applied, moved.Revision)
	stale := apply(machine.KVCompareAndSwap("leader", []byte("fra"), took.Revision))
	fmt.Println(stale.Applied, string(stale.Value), stale.Revision)
	// Output:
	// true 1
	// true 2
	// false par 2
}
