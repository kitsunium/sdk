package service

// soleRoot is GuessMainPID='s guess among a cgroup's members, as systemd's
// unit_search_main_pid makes it: the one member whose parent is not a member —
// the daemon its command left, reparented away, whose workers are its own
// children —, when there is exactly one. The engine and PID 1 are never
// guessed; a member whose parent cannot be read is no candidate.
func soleRoot(members []int, self int) (int, bool) {
	in := make(map[int]bool, len(members))
	//: the set: cgroup.procs may repeat a pid.
	for _, pid := range members {
		in[pid] = true
	}
	root, roots := 0, 0
	//: each member once.
	for pid := range in {
		//: never the engine nor init.
		if pid <= 1 || pid == self {
			continue
		}
		parent, known := parentOf(pid)
		//: gone, or a member's child.
		if !known || in[parent] {
			continue
		}
		root, roots = pid, roots+1
	}
	//: exactly one.
	return root, roots == 1
}
