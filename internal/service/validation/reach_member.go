// Package validation — a field the plan puts rules on, located for the reach
// check.
package validation

// reachMember is a field the plan puts rules on at one object's level: its
// path name, for the refusal, and its index path from that object's struct.
type reachMember struct {
	name  string
	index []int
}
