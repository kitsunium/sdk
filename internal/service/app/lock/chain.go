package lock

import (
	"log"
	"path/filepath"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
	"github.com/kitsunium/sdk/internal/kernel/fs/pathchain"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// kindIndirection names what was found at a parent component.
//
// It is deliberately NOT the platform spelling [openLockFile] uses. A parent
// component is judged through fs.FileMode, which maps a Unix symbolic link, a
// Windows symbolic link and a Windows directory junction onto the same bit —
// so naming one of the three would be a guess, and the `target` field says
// where it actually goes, which is the thing an operator needs.
const kindIndirection string = "indirection"

// checkChain refuses the lock directory when any component of its path is an
// indirection that anybody could have planted.
//
// It runs at CONSTRUCTION, once, and it is an audit of the caller's own
// configuration rather than a guard on a hot path — which is also the limit
// worth stating: a component replaced after this returns is not seen by it.
// The component this package derives, the lock file's own name, is guarded at
// every open instead, by [openLockFile], because that is the one an attacker
// can predict without being told anything.
func checkChain(dir string) error {
	steps, resolveErr := pathchain.Resolve(dir)
	//: the path could not be walked at all — a permission denied on an
	//: ancestor, a file where a directory belongs, an indirection loop.
	if resolveErr != nil {
		//: LockBackendFailed, naming the directory the caller configured.
		return backendFailed("resolve", dir, resolveErr)
	}
	//: one verdict per component that exists. A component that does not exist
	//: yet cannot have been planted, and pathchain.Resolve stops there.
	for _, step := range steps {
		//: an ordinary directory redirects nothing.
		if !step.Indirect {
			//: next component.
			continue
		}
		container := filepath.Dir(step.Path)
		yes, observed := plantable(step.Container, container)
		//: an indirection nobody outside the owner and group could have
		//: created is the operating system's own arrangement — or one whose
		//: container could not be inspected, which accepts and says so.
		if !yes {
			//: an inspection that could not run is accepted and RECORDED. On
			//: Unix this never fires, because a mode is always readable; it is
			//: the Windows DACL query that can refuse to answer.
			noteUninspected(container, observed)
			//: next component.
			continue
		}
		//: LockPathRedirected, naming the component rather than the lock file.
		return parentRedirected(dir, step, observed)
	}
	//: every component is either a real directory or an indirection only a
	//: trusted account could have put there.
	return nil
}

// noteUninspected records that a component's container was accepted without
// having been inspected.
//
// [plantable] answers "not plantable" for a container it judged safe AND for
// one it could not judge at all, because there is no third verdict to return
// and refusing on a platform API failure would cost a caller a locker on a
// directory that is perfectly safe. The two are still different facts, and
// only one of them means the protection ran.
//
// observed is EMPTY whenever a verdict was reached, so nothing here fires on
// the ordinary path — and on Unix it never fires at all, since reading a mode
// cannot fail once the component has been described.
func noteUninspected(container, observed string) {
	//: a verdict was reached.
	if observed == "" {
		//: nothing to record.
		return
	}
	//: no verdict. Accepting is the decision; being quiet about it is not.
	log.Printf("cannot read the access control list of %s (%s); an indirection planted there would not be refused", container, observed)
}

// parentRedirected builds the [corelock.LockPathRedirected] refusal for a component
// ABOVE the lock file.
//
// It names both paths on purpose. The configured directory is what the
// operator typed and will search for; the component is where the redirection
// actually is, and it is usually neither the first nor the last thing they
// would have looked at.
func parentRedirected(dir string, step pathchain.StepValue, observed string) error {
	//: the same sentinel the final component raises, because it is the same
	//: condition and the same remedy: a human looks at the directory, and no
	//: retry helps.
	return kerrs.Wrap(corelock.LockPathRedirected, kerrs.WrapParams{},
		kerrs.String("path", step.Path),
		kerrs.String("dir", dir),
		kerrs.String("kind", kindIndirection),
		kerrs.String("target", step.Target),
		kerrs.String("observed", observed))
}
