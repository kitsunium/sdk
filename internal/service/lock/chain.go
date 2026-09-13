// Package lock — the other half of "the lock path is a file, never a link to
// one": the components ABOVE the lock file.
//
// # O_NOFOLLOW stops at the last component, and that is a kernel limit
//
// [openLockFile] refuses an indirection planted at the lock file's own name.
// It cannot refuse one planted at a PARENT component, because O_NOFOLLOW
// governs the final component only — measured, and recorded in ADR 0082
// §Deferred as the half that stayed open. The whole lock directory moves:
//
//	Dir demandé  = …/pub/myapp/locks
//	composant planté = …/pub/myapp -> …/elsewhere
//	construction ACCEPTÉE
//	acquisition sur parent planté : err=<nil>
//	SUIVI : le verrou a atterri sur …/elsewhere/locks/a4d268….lock
//
// # A link at a parent is not evidence of anything on its own
//
// The obvious remedy — refuse a link anywhere in the path — is wrong, and it
// is wrong on a platform this repository tests on. That is measured rather
// than recited: on the macos-arm64 job of e2e-cross every t.TempDir() resolves
// through /var -> /private/var, which Apple ships. /var/run is one to /run on
// most Linux distributions; C:\Users\All Users is a junction to
// C:\ProgramData. A blanket refusal would turn every lock directory under any
// of them into LOCK_PATH_REDIRECTED, which is ADR 0018 §(a)'s failure mode
// wearing an error that blames the deployment for the operating system's own
// layout.
//
// What separates those from an attack is not the link, it is the directory the
// link LIVES IN. A link in a directory only root can write was put there by
// root. A link in a world-writable directory was put there by anybody at all.
// So the rule is the one ADR 0082 already argued for the final component,
// applied to every component: an indirection is refused when anyone could have
// created it, and the STICKY BIT DOES NOT EXEMPT IT — sticky governs unlinking
// an entry that exists, and planting a component creates one at a name nobody
// has taken.
//
// That is deliberately a different rule from [checkDir]'s, which accepts
// 0777|sticky, and the difference is the same one ADR 0082 turns on:
// checkDir asks who can REPLACE the lock file, this asks who can CREATE a
// component of its path.
package lock

import (
	"log"
	"path/filepath"

	"github.com/kitsunium/sdk/internal/kernel/pathchain"

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

// parentRedirected builds the [LockPathRedirected] refusal for a component
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
	return kerrs.Wrap(LockPathRedirected, kerrs.WrapParams{},
		kerrs.String("path", step.Path),
		kerrs.String("dir", dir),
		kerrs.String("kind", kindIndirection),
		kerrs.String("target", step.Target),
		kerrs.String("observed", observed))
}
