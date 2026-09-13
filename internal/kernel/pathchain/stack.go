// Package pathchain — the walk's POSITION: one open directory handle per
// component already entered, so every question is asked of a descriptor this
// process holds rather than of a path string that could resolve elsewhere.
package pathchain

import (
	"errors"
	"os"
)

// rootStack is the walk's position: one open directory handle per component
// already entered, with the filesystem root at the bottom.
//
// It is a stack rather than a single handle because ".." has to be expressible
// and cannot be asked of os.Root — "openat ..: path escapes from parent" is
// what a Root says to its own parent, which is the whole point of a Root.
// Popping a handle we still hold is the same movement with none of the
// re-resolution a second lookup of the parent's path string would need.
//
// Nothing here is safe for concurrent use; a walk is one goroutine's.
type rootStack struct {
	// roots holds one open handle per level, roots[0] being the filesystem
	// root the walk started from. It is never empty between openStack and
	// close.
	roots []*os.Root
	// names holds the component that was entered to reach each level above
	// the root, so the stack can render the path it currently stands at
	// without re-joining the caller's string.
	names []string
	// base is the path of roots[0] — "/" on Unix, a volume root on Windows.
	base string
	// abandoned accumulates every handle this walk opened and could not give
	// back. Nothing is ever written through these descriptors, so a refused
	// close cannot have lost data — but a filesystem that will not close one
	// is saying something, and this package's consumer reports a refused close
	// for exactly that reason (see internal/service/lock's release).
	abandoned error
}

// openStack opens base and returns a stack standing on it.
func openStack(base string) (stack *rootStack, err error) {
	opened, openErr := os.OpenRoot(base)
	//: the filesystem root could not be opened, which is a medium or
	//: permission failure and not something a walk can work around.
	if openErr != nil {
		//: the filesystem's own error.
		return nil, openErr
	}
	//: standing on the root, having entered nothing.
	return &rootStack{roots: []*os.Root{opened}, base: base}, nil
}

// top returns the handle the walk currently stands on.
func (s *rootStack) top() *os.Root {
	//: never empty: openStack seeds it and pop refuses to empty it.
	return s.roots[len(s.roots)-1]
}

// path renders where the walk currently stands.
func (s *rootStack) path() string {
	//: the handle knows its own name, which os.Root built by joining as it
	//: descended — so this is the resolved path rather than the caller's.
	return s.top().Name()
}

// push enters name, adding a level to the stack.
func (s *rootStack) push(name string) error {
	entered, enterErr := s.top().OpenRoot(name)
	//: not a directory, or not one this process may enter.
	if enterErr != nil {
		//: the filesystem's own error; the stack is unchanged.
		return enterErr
	}
	s.roots = append(s.roots, entered)
	s.names = append(s.names, name)
	//: entered.
	return nil
}

// pop leaves the current level, or stays at the root.
func (s *rootStack) pop() {
	//: "/.." is "/" on every kernel here, so the root never pops.
	if len(s.roots) == 1 {
		//: already at the bottom.
		return
	}
	last := len(s.roots) - 1
	//: the handle is finished with; a walk of a deep path would otherwise hold
	//: one descriptor per component until it returned.
	s.closeAll(s.roots[last : last+1])
	s.roots = s.roots[:last]
	s.names = s.names[:last]
}

// reset unwinds to base and reopens the stack there.
//
// It is what an absolute indirection target needs: resolution restarts at a
// root, and on Windows that may not even be the root the walk started from,
// because a junction may point at another volume.
func (s *rootStack) reset(base string) error {
	opened, openErr := os.OpenRoot(base)
	//: the target's root could not be opened; the stack is left usable so the
	//: deferred close still has something to close.
	if openErr != nil {
		//: the filesystem's own error.
		return openErr
	}
	//: the handles below the restart are finished with. What they could not
	//: give back stays on `abandoned` and reaches the caller through close.
	s.closeAll(s.roots)
	s.roots = []*os.Root{opened}
	s.names = nil
	s.base = base
	//: standing on the target's root.
	return nil
}

// close releases every handle the stack holds and reports every one it could
// not give back, including those a pop released earlier.
func (s *rootStack) close() error {
	s.closeAll(s.roots)
	s.roots = nil
	s.names = nil
	//: nil when every descriptor came back, which is the ordinary outcome.
	return s.abandoned
}

// closeAll releases directory handles, accumulating what it cannot.
func (s *rootStack) closeAll(roots []*os.Root) {
	//: from the top of the stack down, which is the reverse of the order they
	//: were opened in — irrelevant to the kernel, and what a reader expects.
	for _, root := range roots {
		closeErr := root.Close()
		//: a refused close is carried rather than dropped, and joined rather
		//: than replaced, so a walk that lost several says several.
		s.abandoned = errors.Join(s.abandoned, closeErr)
	}
}
