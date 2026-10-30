package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #763: #603's guard trusted the configuration it last heard a voter at for
// as long as the leader's log resolved that index, the voter down or not. A
// voter down may come back over a truncated log (TruncateAndStaySilent),
// and the offline refusal of #758 reads only that voter's own log, which
// holds no change committed while it was down: a founder heard at
// {B, V, D, E}, B removed while it was down, came back silent over
// {A, B, V}, refused D and E, and stayed silent for good. A voter that no
// longer answers is judged as one never heard. The file is named after the
// simulator's so that the shards of its seed parts stay where they are.

// Over a compacted log, voter 2 answers at the last index: the removal of
// 3 is admitted. Voter 2 then stops answering past the vote window while
// 3, 4 and 5 keep answering: its configuration is unknown again, and the
// removal of 3 is refused. Once it answers again, the removal goes through.
func TestARemovalIsJudgedOnAVotersConfigurationOnlyWhileItAnswers(t *testing.T) {
	r := leaderOverCompacted(0)
	ackFrom(r, 2, r.log.lastIndex())
	if !r.answering(2) || r.strands(r.conf()) {
		t.Fatalf("setup: voter 2 answering %v, stranded %v", r.answering(2), r.strands(r.conf()))
	}
	r.now = r.now.Add(r.voteWindow + time.Millisecond)
	for _, id := range []core.NodeID{3, 4, 5} {
		ackFrom(r, id, r.log.lastIndex())
	}
	if r.answering(2) {
		t.Fatal("setup: voter 2 still answers")
	}
	if err := proposeRemove(r, 3); err == nil {
		t.Fatal("voter 2, silent past the vote window, may come back holding a configuration the compacted log no longer tells, yet 3, which answers, was removed")
	}
	ackFrom(r, 2, r.log.lastIndex())
	if err := proposeRemove(r, 3); err != nil {
		t.Fatalf("voter 2 answers again at the last index and knows leader 1, yet removing 3 was %v", err)
	}
}
